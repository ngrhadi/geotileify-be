package tile

import (
	"archive/zip"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func requireTilingTools(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping external-tool integration test in -short mode")
	}
	for _, bin := range []string{duckdbBin(), tylertooBin()} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not installed; skipping integration test", bin)
		}
	}
}

func TestLayerName(t *testing.T) {
	cases := map[string]string{
		"jakarta-roads.geojson": "jakarta_roads",
		"My File (1).FGB":       "my_file_1",
		"---.geojson":           "geotileify",
		".geojson":              "geotileify",
	}
	for input, want := range cases {
		if got := LayerName(input); got != want {
			t.Errorf("LayerName(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestPipelineFormats(t *testing.T) {
	requireTilingTools(t)

	dir := t.TempDir()
	geojsonPath := writeFile(t, dir, "input.geojson", mixedGeoJSON)
	geojsonlPath := writeFile(t, dir, "input.geojsonl", mixedGeoJSONLines(t))
	geojsonsPath := writeFile(t, dir, "input.geojsons", mixedGeoJSONSequence(t))

	fgbPath := filepath.Join(dir, "input.fgb")
	if err := runDuckDB(fmt.Sprintf(
		`LOAD spatial; COPY (SELECT * FROM ST_Read(%s)) TO %s WITH (FORMAT GDAL, DRIVER 'FlatGeobuf');`,
		sqlString(geojsonPath), sqlString(fgbPath),
	)); err != nil {
		t.Fatalf("create FlatGeobuf fixture: %v", err)
	}

	pointsPath := writeFile(t, dir, "points.geojson", pointsGeoJSON)
	shpDir := filepath.Join(dir, "shp")
	if err := os.MkdirAll(shpDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := runDuckDB(fmt.Sprintf(
		`LOAD spatial; COPY (SELECT * FROM ST_Read(%s)) TO %s WITH (FORMAT GDAL, DRIVER 'ESRI Shapefile');`,
		sqlString(pointsPath), sqlString(filepath.Join(shpDir, "points.shp")),
	)); err != nil {
		t.Fatalf("create Shapefile fixture: %v", err)
	}
	zipPath := filepath.Join(dir, "points-shp.zip")
	zipDir(t, shpDir, zipPath)

	cases := []struct {
		name    string
		input   string
		convert func(input, output string) error
	}{
		{"geojson", geojsonPath, ConvertToGeoParquet},
		{"geojsonl", geojsonlPath, ConvertToGeoParquet},
		{"geojsons", geojsonsPath, ConvertToGeoParquet},
		{"flatgeobuf", fgbPath, ConvertToGeoParquet},
		{"shapefile-zip", zipPath, ConvertShapefileZipToGeoParquet},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			parquetPath := filepath.Join(dir, tc.name+".parquet")
			if err := tc.convert(tc.input, parquetPath); err != nil {
				t.Fatalf("convert: %v", err)
			}
			if err := requireNonEmpty(parquetPath, "geoparquet"); err != nil {
				t.Fatal(err)
			}
			pmtilesPath := filepath.Join(dir, tc.name+".pmtiles")
			if err := GeneratePMTiles(parquetPath, pmtilesPath, Options{
				MinZoom:   0,
				MaxZoom:   5,
				LayerName: tc.name,
			}); err != nil {
				t.Fatalf("tile: %v", err)
			}
			assertPMTiles(t, pmtilesPath)
		})
	}
}

func TestPrepareGeoParquetPassthrough(t *testing.T) {
	requireTilingTools(t)

	dir := t.TempDir()
	geojsonPath := writeFile(t, dir, "input.geojson", mixedGeoJSON)
	parquetPath := filepath.Join(dir, "input.parquet")
	if err := ConvertToGeoParquet(geojsonPath, parquetPath); err != nil {
		t.Fatalf("convert: %v", err)
	}

	prepared, err := PrepareGeoParquet(parquetPath, filepath.Join(dir, "prepared.parquet"))
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if prepared != parquetPath {
		t.Fatalf("expected WGS84 parquet to pass through %q, got %q", parquetPath, prepared)
	}

	pmtilesPath := filepath.Join(dir, "parquet.pmtiles")
	if err := GeneratePMTiles(prepared, pmtilesPath, Options{
		MinZoom:   0,
		MaxZoom:   5,
		LayerName: "parquet",
	}); err != nil {
		t.Fatalf("tile: %v", err)
	}
	assertPMTiles(t, pmtilesPath)
}

func TestProjectedWithoutCRSIsRejected(t *testing.T) {
	requireTilingTools(t)

	dir := t.TempDir()
	geojsonPath := writeFile(t, dir, "input.geojson", pointsGeoJSON)
	fgbPath := filepath.Join(dir, "projected.fgb")
	if err := runDuckDB(fmt.Sprintf(
		`LOAD spatial; COPY (
			SELECT * REPLACE (
				ST_Transform(geom, 'EPSG:4326', 'EPSG:3857', true) AS geom
			)
			FROM ST_Read(%s)
		) TO %s WITH (FORMAT GDAL, DRIVER 'FlatGeobuf');`,
		sqlString(geojsonPath), sqlString(fgbPath),
	)); err != nil {
		t.Fatalf("create projected fixture: %v", err)
	}

	err := ConvertToGeoParquet(fgbPath, filepath.Join(dir, "out.parquet"))
	if err == nil {
		t.Fatal("expected an error for projected coordinates without a declared CRS")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "crs") {
		t.Fatalf("expected CRS guidance in error, got: %v", err)
	}
}

func TestParquetSourceCRS(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantCRS string
		known   bool
		isWGS84 bool
		assumed bool
	}{
		{"absent", "null", "OGC:CRS84", true, true, true},
		{"epsg4326", `{"id":{"authority":"EPSG","code":4326}}`, "EPSG:4326", true, true, false},
		{"epsg3857", `{"id":{"authority":"EPSG","code":"3857"}}`, "EPSG:3857", true, false, false},
		{"ogc-crs84", `{"id":{"authority":"OGC","code":"CRS84"}}`, "OGC:CRS84", true, true, false},
		{"string-crs", `"EPSG:3857"`, "EPSG:3857", true, false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			crs, known, isWGS84, assumed := parquetSourceCRS([]byte(tt.raw))
			if crs != tt.wantCRS || known != tt.known || isWGS84 != tt.isWGS84 || assumed != tt.assumed {
				t.Fatalf("got (%q, %v, %v, %v), want (%q, %v, %v, %v)",
					crs, known, isWGS84, assumed, tt.wantCRS, tt.known, tt.isWGS84, tt.assumed)
			}
		})
	}
}

func TestOptionsValidate(t *testing.T) {
	valid := []Options{
		{MinZoom: 0, MaxZoom: 14, SimplifyFactor: 1.0},
		{MinZoom: 0, MaxZoom: MaxAllowedZoom, SimplifyFactor: 0.1},
		{MinZoom: 5, MaxZoom: 5, SimplifyFactor: 4.0},
	}
	for _, opts := range valid {
		if err := opts.Validate(); err != nil {
			t.Errorf("Validate(%+v) = %v, want nil", opts, err)
		}
	}

	invalid := []Options{
		{MinZoom: -1, MaxZoom: 14, SimplifyFactor: 1.0},
		{MinZoom: 0, MaxZoom: MaxAllowedZoom + 1, SimplifyFactor: 1.0},
		{MinZoom: 10, MaxZoom: 5, SimplifyFactor: 1.0},
		{MinZoom: 0, MaxZoom: 14, SimplifyFactor: -1},
		{MinZoom: 0, MaxZoom: 14, SimplifyFactor: 11},
	}
	for _, opts := range invalid {
		if err := opts.Validate(); err == nil {
			t.Errorf("Validate(%+v) = nil, want error", opts)
		}
	}
}

func assertPMTiles(t *testing.T, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read PMTiles: %v", err)
	}
	if len(data) < 2 || string(data[:2]) != "PM" {
		t.Fatalf("%s does not start with the PMTiles magic bytes", path)
	}
}

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func zipDir(t *testing.T, srcDir, zipPath string) {
	t.Helper()
	out, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()

	zw := zip.NewWriter(out)
	entries, err := os.ReadDir(srcDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		path := filepath.Join(srcDir, entry.Name())
		info, err := entry.Info()
		if err != nil {
			t.Fatal(err)
		}
		header, err := zip.FileInfoHeader(info)
		if err != nil {
			t.Fatal(err)
		}
		header.Name = entry.Name()
		header.Method = zip.Deflate
		w, err := zw.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		in, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.Copy(w, in); err != nil {
			in.Close()
			t.Fatal(err)
		}
		in.Close()
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
}

func mixedGeoJSONLines(t *testing.T) string {
	t.Helper()
	var fc struct {
		Features []json.RawMessage `json:"features"`
	}
	if err := json.Unmarshal([]byte(mixedGeoJSON), &fc); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, feature := range fc.Features {
		b.Write(feature)
		b.WriteByte('\n')
	}
	return b.String()
}

func mixedGeoJSONSequence(t *testing.T) string {
	t.Helper()
	var fc struct {
		Features []json.RawMessage `json:"features"`
	}
	if err := json.Unmarshal([]byte(mixedGeoJSON), &fc); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, feature := range fc.Features {
		b.WriteByte(0x1e)
		b.Write(feature)
		b.WriteByte('\n')
	}
	return b.String()
}

const pointsGeoJSON = `{"type":"FeatureCollection","features":[
{"type":"Feature","properties":{"name":"a","pop":1},"geometry":{"type":"Point","coordinates":[106.8,-6.2]}},
{"type":"Feature","properties":{"name":"b","pop":2},"geometry":{"type":"Point","coordinates":[106.9,-6.3]}}
]}`

const mixedGeoJSON = `{"type":"FeatureCollection","features":[
{"type":"Feature","properties":{"name":"a","pop":1},"geometry":{"type":"Point","coordinates":[106.8,-6.2]}},
{"type":"Feature","properties":{"name":"b","pop":2},"geometry":{"type":"Point","coordinates":[106.9,-6.3]}},
{"type":"Feature","properties":{"name":"c","pop":3},"geometry":{"type":"LineString","coordinates":[[106.7,-6.1],[106.75,-6.15]]}},
{"type":"Feature","properties":{"name":"d","pop":4},"geometry":{"type":"Polygon","coordinates":[[[106.6,-6.0],[106.65,-6.0],[106.65,-6.05],[106.6,-6.05],[106.6,-6.0]]]}}
]}`

func TestMetadataAndPropertyOptions(t *testing.T) {
	requireTilingTools(t)

	dir := t.TempDir()
	geojsonPath := writeFile(t, dir, "input.geojson", mixedGeoJSON)
	parquetPath := filepath.Join(dir, "input.parquet")
	if err := ConvertToGeoParquet(geojsonPath, parquetPath); err != nil {
		t.Fatalf("convert: %v", err)
	}

	t.Run("metadata + simplify", func(t *testing.T) {
		pmtilesPath := filepath.Join(dir, "metadata.pmtiles")
		if err := GeneratePMTiles(parquetPath, pmtilesPath, Options{
			MinZoom:        0,
			MaxZoom:        5,
			LayerName:      "metadata",
			SimplifyFactor: 0.5,
			Metadata: MetadataFields{
				Name:        "My Dataset",
				Description: "A test archive",
				Attribution: "© Test",
			},
		}); err != nil {
			t.Fatalf("tile: %v", err)
		}

		meta := readPMTilesMetadataForTest(t, pmtilesPath)
		if meta["name"] != "My Dataset" {
			t.Fatalf("name = %v", meta["name"])
		}
		if meta["description"] != "A test archive" {
			t.Fatalf("description = %v", meta["description"])
		}
		if meta["attribution"] != "© Test" {
			t.Fatalf("attribution = %v", meta["attribution"])
		}
		if _, ok := meta["vector_layers"]; !ok {
			t.Fatal("vector_layers metadata was not preserved")
		}
	})

	t.Run("exclude all properties", func(t *testing.T) {
		pmtilesPath := filepath.Join(dir, "no-props.pmtiles")
		if err := GeneratePMTiles(parquetPath, pmtilesPath, Options{
			MinZoom:              0,
			MaxZoom:              5,
			LayerName:            "noprops",
			SimplifyFactor:       1.0,
			ExcludeAllProperties: true,
		}); err != nil {
			t.Fatalf("tile: %v", err)
		}
		assertLayerFields(t, readPMTilesMetadataForTest(t, pmtilesPath), nil)
	})

	t.Run("include properties overrides exclude all", func(t *testing.T) {
		pmtilesPath := filepath.Join(dir, "one-prop.pmtiles")
		if err := GeneratePMTiles(parquetPath, pmtilesPath, Options{
			MinZoom:              0,
			MaxZoom:              5,
			LayerName:            "oneprop",
			SimplifyFactor:       1.0,
			ExcludeAllProperties: true,
			IncludeProperties:    []string{"name"},
		}); err != nil {
			t.Fatalf("tile: %v", err)
		}
		assertLayerFields(t, readPMTilesMetadataForTest(t, pmtilesPath), []string{"name"})
	})
}

func readPMTilesMetadataForTest(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) < 127 || string(data[0:7]) != "PMTiles" || data[7] != 3 {
		t.Fatal("not a PMTiles v3 archive")
	}
	jsonOffset := binary.LittleEndian.Uint64(data[24:32])
	jsonLength := binary.LittleEndian.Uint64(data[32:40])
	raw, err := decompressMetadata(data[jsonOffset:jsonOffset+jsonLength], data[97])
	if err != nil {
		t.Fatal(err)
	}
	var meta map[string]any
	if err := json.Unmarshal(raw, &meta); err != nil {
		t.Fatal(err)
	}
	return meta
}

func assertLayerFields(t *testing.T, meta map[string]any, want []string) {
	t.Helper()
	layers, ok := meta["vector_layers"].([]any)
	if !ok || len(layers) == 0 {
		t.Fatalf("vector_layers missing: %v", meta["vector_layers"])
	}
	layer, ok := layers[0].(map[string]any)
	if !ok {
		t.Fatalf("vector layer is not an object: %T", layers[0])
	}
	fields, _ := layer["fields"].(map[string]any)
	if len(fields) != len(want) {
		t.Fatalf("fields = %v, want %v", fields, want)
	}
	for _, name := range want {
		if _, ok := fields[name]; !ok {
			t.Fatalf("fields = %v, want %v", fields, want)
		}
	}
}
