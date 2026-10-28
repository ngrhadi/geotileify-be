package tile

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
)

// Tylertoo reads GeoParquet and writes PMTiles. DuckDB Spatial is used to
// normalise every supported upload format to GeoParquet first:
//
//	GeoJSON / JSON / GeoJSONL / GeoJSONS / FlatGeobuf / Shapefile
//	    -> ST_Read(...) -> GeoParquet
//	GeoParquet
//	    -> used directly when it is already WGS84; otherwise reprojected
//	Shapefile ZIP
//	    -> validated + extracted -> ST_Read(.shp) -> GeoParquet
//
// There is intentionally no fallback tiler in this pipeline.

const (
	DefaultMinZoom = 0
	DefaultMaxZoom = 14
	MaxAllowedZoom = 16
)

// Options controls a single PMTiles build.
type Options struct {
	MinZoom              int
	MaxZoom              int
	LayerName            string
	SimplifyFactor       float64
	ExcludeAllProperties bool
	IncludeProperties    []string
	Metadata             MetadataFields
}

// Validate checks the zoom range and simplification factor accepted by the API
// and the popup UI.
func (o Options) Validate() error {
	if o.MinZoom < 0 || o.MaxZoom > MaxAllowedZoom || o.MinZoom > o.MaxZoom {
		return fmt.Errorf("zoom range must be 0-%d with min_zoom <= max_zoom", MaxAllowedZoom)
	}
	if o.SimplifyFactor < 0 || o.SimplifyFactor > 10 {
		return fmt.Errorf("simplify_factor must be between 0 and 10")
	}
	return nil
}

func duckdbBin() string {
	if v := strings.TrimSpace(os.Getenv("DUCKDB_BIN")); v != "" {
		return v
	}
	return "duckdb"
}

func tylertooBin() string {
	if v := strings.TrimSpace(os.Getenv("TYLERTOO_BIN")); v != "" {
		return v
	}
	return "tylertoo"
}

// CheckDependencies fails fast when the external conversion tools are missing.
// The server cannot generate tiles without both DuckDB and tylertoo.
func CheckDependencies() error {
	for _, bin := range []string{duckdbBin(), tylertooBin()} {
		if _, err := exec.LookPath(bin); err != nil {
			return fmt.Errorf("%s not found in PATH: %w", bin, err)
		}
	}
	return nil
}

// GeneratePMTiles runs tylertoo on a GeoParquet input.
func GeneratePMTiles(inputParquet, output string, opts Options) error {
	if opts.LayerName == "" {
		opts.LayerName = "geotileify"
	}
	if opts.SimplifyFactor == 0 {
		opts.SimplifyFactor = 1.0
	}
	if err := opts.Validate(); err != nil {
		return err
	}

	args := []string{
		inputParquet,
		output,
		"--min-zoom", strconv.Itoa(opts.MinZoom),
		"--max-zoom", strconv.Itoa(opts.MaxZoom),
		"--simplify-factor", strconv.FormatFloat(opts.SimplifyFactor, 'f', -1, 64),
		"--layer-name", opts.LayerName,
		// Legacy tiler default was 5; keep visual seams close.
		"--tile-buffer", "5",
		"--force",
	}
	if len(opts.IncludeProperties) > 0 {
		// --include-property overrides --exclude-all-properties.
		for _, property := range opts.IncludeProperties {
			args = append(args, "--include-property", property)
		}
	} else if opts.ExcludeAllProperties {
		args = append(args, "--exclude-all-properties")
	}

	cmd := exec.Command(tylertooBin(), args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("tylertoo: %w: %s", err, strings.TrimSpace(string(out)))
	}

	fi, err := os.Stat(output)
	if err != nil {
		return fmt.Errorf("tylertoo output missing: %w", err)
	}
	if fi.Size() == 0 {
		return fmt.Errorf("tylertoo produced an empty archive")
	}
	if err := SetPMTilesMetadata(output, opts.Metadata); err != nil {
		return fmt.Errorf("inject PMTiles metadata: %w", err)
	}
	return nil
}

// LayerName turns an uploaded filename into a safe, readable MVT layer name.
func LayerName(filename string) string {
	base := strings.TrimSuffix(filepath.Base(filename), filepath.Ext(filename))
	var b strings.Builder
	lastUnderscore := false
	for _, r := range strings.ToLower(base) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			lastUnderscore = false
		case !lastUnderscore:
			b.WriteByte('_')
			lastUnderscore = true
		}
	}
	name := strings.Trim(b.String(), "_")
	if name == "" {
		return "geotileify"
	}
	if len([]rune(name)) > 64 {
		name = string([]rune(name)[:64])
	}
	return name
}

// spatialMeta is the subset of ST_Read_Meta() needed to normalise a vector file.
type spatialMeta struct {
	GeomName string  `json:"geom_name"`
	AuthName *string `json:"auth_name"`
	AuthCode *string `json:"auth_code"`
	WKT      *string `json:"wkt"`
}

// ConvertToGeoParquet converts a GeoJSON/JSON/GeoJSONL/GeoJSONS/FlatGeobuf/
// Shapefile input into a WGS84 GeoParquet file via DuckDB Spatial.
func ConvertToGeoParquet(input, output string) error {
	meta, err := readSpatialMeta(input)
	if err != nil {
		return err
	}

	geom := quoteIdent(meta.GeomName)
	geomExpr := geom

	crs, known, isWGS84 := spatialSourceCRS(meta)
	if known && !isWGS84 {
		// always_xy := true keeps lon/lat axis order (PROJ's default otherwise
		// swaps axes for many projected CRSs).
		geomExpr = fmt.Sprintf(
			"ST_Transform(%s, %s, 'EPSG:4326', true)",
			geom, sqlString(crs),
		)
	} else if !known {
		looksWGS84, err := spatialLooksWGS84(input, meta.GeomName)
		if err != nil {
			return err
		}
		if !looksWGS84 {
			return fmt.Errorf(
				"source CRS is not declared and the coordinates look projected; " +
					"include a .prj/CRS or reproject the data to EPSG:4326",
			)
		}
	}

	query := fmt.Sprintf(
		`LOAD spatial; COPY (SELECT * REPLACE (%s AS %s) FROM ST_Read(%s)) TO %s (FORMAT parquet);`,
		geomExpr, geom, sqlString(input), sqlString(output),
	)
	if err := runDuckDB(query); err != nil {
		return err
	}
	return requireNonEmpty(output, "duckdb geoparquet output")
}

// PrepareGeoParquet handles an uploaded .parquet:
//   - valid GeoParquet already in WGS84 (or with the spec's implicit CRS84)
//     is passed through unchanged;
//   - GeoParquet in another CRS is reprojected to WGS84 via DuckDB;
//   - non-GeoParquet files are rejected with a clear error.
func PrepareGeoParquet(input, output string) (string, error) {
	meta, err := readParquetGeoMeta(input)
	if err != nil {
		return "", err
	}

	col, ok := meta.Columns[meta.PrimaryColumn]
	if !ok {
		return "", fmt.Errorf("GeoParquet geometry column %q is not declared", meta.PrimaryColumn)
	}

	crs, known, isWGS84, assumed := parquetSourceCRS(col.CRS)
	if assumed && bboxLooksProjected(col.BBox) {
		return "", fmt.Errorf(
			"GeoParquet has no CRS but its bbox looks projected; " +
				"set the CRS in the file or reproject to EPSG:4326",
		)
	}
	if !known {
		return "", fmt.Errorf("GeoParquet CRS could not be determined")
	}
	if isWGS84 {
		return input, nil
	}

	geom := quoteIdent(meta.PrimaryColumn)
	query := fmt.Sprintf(
		`LOAD spatial; COPY (
			SELECT * REPLACE (
				ST_Transform(%s, %s, 'EPSG:4326', true) AS %s
			)
			FROM read_parquet(%s)
		) TO %s (FORMAT parquet);`,
		geom, sqlString(crs), geom, sqlString(input), sqlString(output),
	)
	if err := runDuckDB(query); err != nil {
		return "", err
	}
	if err := requireNonEmpty(output, "duckdb reprojected parquet"); err != nil {
		return "", err
	}
	return output, nil
}

func readSpatialMeta(input string) (spatialMeta, error) {
	query := fmt.Sprintf(
		`LOAD spatial;
		 SELECT layers[1].geometry_fields[1].name AS geom_name,
		        layers[1].geometry_fields[1].crs.auth_name AS auth_name,
		        layers[1].geometry_fields[1].crs.auth_code AS auth_code,
		        layers[1].geometry_fields[1].crs.wkt AS wkt
		 FROM ST_Read_Meta(%s);`,
		sqlString(input),
	)
	raw, err := runDuckDBJSON(query)
	if err != nil {
		return spatialMeta{}, err
	}
	var rows []spatialMeta
	if len(strings.TrimSpace(string(raw))) > 0 {
		if err := json.Unmarshal(raw, &rows); err != nil {
			return spatialMeta{}, fmt.Errorf("parse ST_Read_Meta: %w", err)
		}
	}
	if len(rows) == 0 || rows[0].GeomName == "" {
		return spatialMeta{}, fmt.Errorf("no geometry column found in %s", filepath.Base(input))
	}
	return rows[0], nil
}

func spatialSourceCRS(meta spatialMeta) (crs string, known, isWGS84 bool) {
	if meta.AuthName != nil && meta.AuthCode != nil && strings.TrimSpace(*meta.AuthCode) != "" {
		auth := strings.ToUpper(strings.TrimSpace(*meta.AuthName))
		code := strings.TrimSpace(*meta.AuthCode)
		if auth == "OGC" && strings.EqualFold(code, "CRS84") {
			return "OGC:CRS84", true, true
		}
		if auth == "EPSG" {
			if code == "4326" {
				return "EPSG:4326", true, true
			}
			return "EPSG:" + code, true, false
		}
		if meta.WKT != nil && strings.TrimSpace(*meta.WKT) != "" {
			return *meta.WKT, true, false
		}
		return auth + ":" + code, true, false
	}
	if meta.WKT != nil && strings.TrimSpace(*meta.WKT) != "" {
		return *meta.WKT, true, false
	}
	return "", false, false
}

func spatialLooksWGS84(input, geomName string) (bool, error) {
	geom := quoteIdent(geomName)
	query := fmt.Sprintf(
		`LOAD spatial;
		 SELECT MIN(ST_XMin(%s)) AS xmin, MAX(ST_XMax(%s)) AS xmax,
		        MIN(ST_YMin(%s)) AS ymin, MAX(ST_YMax(%s)) AS ymax
		 FROM ST_Read(%s);`,
		geom, geom, geom, geom, sqlString(input),
	)
	raw, err := runDuckDBJSON(query)
	if err != nil {
		return false, err
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return false, fmt.Errorf("no features found in %s", filepath.Base(input))
	}
	var rows []struct {
		XMin *float64 `json:"xmin"`
		XMax *float64 `json:"xmax"`
		YMin *float64 `json:"ymin"`
		YMax *float64 `json:"ymax"`
	}
	if err := json.Unmarshal(raw, &rows); err != nil {
		return false, fmt.Errorf("parse geometry bounds: %w", err)
	}
	if len(rows) == 0 || rows[0].XMin == nil || rows[0].XMax == nil || rows[0].YMin == nil || rows[0].YMax == nil {
		return false, fmt.Errorf("no geometry bounds found in %s", filepath.Base(input))
	}
	r := rows[0]
	return *r.XMin >= -180.000001 && *r.XMax <= 180.000001 &&
		*r.YMin >= -90.000001 && *r.YMax <= 90.000001, nil
}

type parquetGeoMeta struct {
	Version       string                      `json:"version"`
	PrimaryColumn string                      `json:"primary_column"`
	Columns       map[string]parquetGeoColumn `json:"columns"`
}

type parquetGeoColumn struct {
	Encoding string          `json:"encoding"`
	BBox     []float64       `json:"bbox"`
	CRS      json.RawMessage `json:"crs"`
}

func readParquetGeoMeta(input string) (parquetGeoMeta, error) {
	query := fmt.Sprintf(
		`SELECT decode(value) AS geo FROM parquet_kv_metadata(%s) WHERE key = 'geo';`,
		sqlString(input),
	)
	raw, err := runDuckDBJSON(query)
	if err != nil {
		return parquetGeoMeta{}, err
	}
	var rows []struct {
		Geo string `json:"geo"`
	}
	if len(strings.TrimSpace(string(raw))) > 0 {
		if err := json.Unmarshal(raw, &rows); err != nil {
			return parquetGeoMeta{}, fmt.Errorf("parse parquet metadata: %w", err)
		}
	}
	if len(rows) == 0 || strings.TrimSpace(rows[0].Geo) == "" {
		return parquetGeoMeta{}, fmt.Errorf(
			"%s is not GeoParquet (missing geo metadata); export it as GeoParquet first",
			filepath.Base(input),
		)
	}
	var meta parquetGeoMeta
	if err := json.Unmarshal([]byte(rows[0].Geo), &meta); err != nil {
		return parquetGeoMeta{}, fmt.Errorf("parse GeoParquet geo metadata: %w", err)
	}
	if meta.PrimaryColumn == "" {
		return parquetGeoMeta{}, fmt.Errorf("GeoParquet metadata has no primary_column")
	}
	return meta, nil
}

func parquetSourceCRS(raw json.RawMessage) (crs string, known, isWGS84, assumed bool) {
	if len(raw) == 0 || strings.TrimSpace(string(raw)) == "null" {
		// The GeoParquet spec defaults a missing CRS to OGC:CRS84.
		return "OGC:CRS84", true, true, true
	}

	// Some writers store CRS as a plain string (e.g. "EPSG:4326").
	var asString string
	if err := json.Unmarshal(raw, &asString); err == nil {
		return crsFromString(asString)
	}

	var obj struct {
		ID *struct {
			Authority string          `json:"authority"`
			Code      json.RawMessage `json:"code"`
		} `json:"id"`
	}
	if err := json.Unmarshal(raw, &obj); err == nil && obj.ID != nil && obj.ID.Authority != "" {
		code := rawJSONScalar(obj.ID.Code)
		auth := strings.ToUpper(strings.TrimSpace(obj.ID.Authority))
		if auth == "OGC" && strings.EqualFold(code, "CRS84") {
			return "OGC:CRS84", true, true, false
		}
		if auth == "EPSG" && code == "4326" {
			return "EPSG:4326", true, true, false
		}
		return auth + ":" + code, true, false, false
	}

	// No id: hand the PROJJSON object straight to PROJ via ST_Transform.
	return string(raw), true, false, false
}

func crsFromString(s string) (crs string, known, isWGS84, assumed bool) {
	s = strings.TrimSpace(s)
	upper := strings.ToUpper(s)
	switch {
	case upper == "":
		return "OGC:CRS84", true, true, true
	case upper == "OGC:CRS84":
		return "OGC:CRS84", true, true, false
	case upper == "EPSG:4326":
		return "EPSG:4326", true, true, false
	default:
		return s, true, false, false
	}
}

func rawJSONScalar(raw json.RawMessage) string {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	return strings.Trim(strings.TrimSpace(string(raw)), `"`)
}

func bboxLooksProjected(bbox []float64) bool {
	if len(bbox) < 4 {
		return false
	}
	return bbox[0] < -180.000001 || bbox[2] > 180.000001 ||
		bbox[1] < -90.000001 || bbox[3] > 90.000001
}

func runDuckDB(query string) error {
	cmd := exec.Command(duckdbBin(), "-bail", "-c", query)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("duckdb: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func runDuckDBJSON(query string) ([]byte, error) {
	cmd := exec.Command(duckdbBin(), "-json", "-c", query)
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("duckdb: %w: %s", err, strings.TrimSpace(string(ee.Stderr)))
		}
		return nil, fmt.Errorf("duckdb: %w", err)
	}
	return out, nil
}

func requireNonEmpty(path, label string) error {
	fi, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("%s missing: %w", label, err)
	}
	if fi.Size() == 0 {
		return fmt.Errorf("%s is empty", label)
	}
	return nil
}

func sqlString(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

func quoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}
