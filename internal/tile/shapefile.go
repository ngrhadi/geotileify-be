package tile

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ConvertShapefileZipToGeoParquet validates a zipped Shapefile bundle, extracts
// it safely, then converts the first .shp to WGS84 GeoParquet via DuckDB.
func ConvertShapefileZipToGeoParquet(zipPath, output string) error {
	extractDir := zipPath + "_extracted"
	shpPath, err := validateAndExtractShapefileZip(zipPath, extractDir)
	if err != nil {
		return err
	}
	defer os.RemoveAll(extractDir)

	return ConvertToGeoParquet(shpPath, output)
}

// shapefileAllowedExts is the whitelist of file extensions permitted inside a
// shapefile zip. Anything outside this list is rejected as potentially unsafe.
var shapefileAllowedExts = map[string]bool{
	".shp": true, // geometry
	".dbf": true, // attributes
	".shx": true, // geometry index
	".prj": true, // coordinate reference system
	".cpg": true, // character encoding
	".sbn": true, // spatial index
	".sbx": true, // spatial index
	".qpj": true, // QGIS projection
	".qix": true, // QGIS spatial index
	".atx": true, // attribute index
	".xml": true, // metadata
}

const (
	maxZipFiles     = 20
	maxExtractBytes = 200 * 1024 * 1024 // 200 MB — guards against zip bombs
)

// validateAndExtractShapefileZip opens the zip, validates its contents, extracts
// to destDir, and returns the path to the .shp file.
func validateAndExtractShapefileZip(zipPath, destDir string) (string, error) {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return "", fmt.Errorf("invalid zip file: %w", err)
	}
	defer r.Close()

	// Reject suspiciously large archives before extracting (zip bomb check).
	if len(r.File) > maxZipFiles {
		return "", fmt.Errorf("zip contains %d files; a shapefile bundle should have at most %d", len(r.File), maxZipFiles)
	}

	var (
		totalSize              uint64
		hasShp, hasDbf, hasShx bool
	)

	for _, f := range r.File {
		if f.FileInfo().IsDir() {
			continue
		}

		ext := strings.ToLower(filepath.Ext(f.Name))
		if !shapefileAllowedExts[ext] {
			return "", fmt.Errorf("unexpected file %q in zip: only shapefile-related files are allowed", filepath.Base(f.Name))
		}

		// Accumulate uncompressed size to detect zip bombs.
		totalSize += f.UncompressedSize64
		if totalSize > maxExtractBytes {
			return "", fmt.Errorf("zip uncompressed content exceeds 200 MB")
		}

		switch ext {
		case ".shp":
			hasShp = true
		case ".dbf":
			hasDbf = true
		case ".shx":
			hasShx = true
		}
	}

	// Ensure all required Shapefile components are present.
	var missing []string
	if !hasShp {
		missing = append(missing, ".shp")
	}
	if !hasDbf {
		missing = append(missing, ".dbf")
	}
	if !hasShx {
		missing = append(missing, ".shx")
	}
	if len(missing) > 0 {
		return "", fmt.Errorf("zip is missing required shapefile components: %s", strings.Join(missing, ", "))
	}

	if err := extractZip(r, destDir); err != nil {
		return "", err
	}

	return findFileByExt(destDir, ".shp")
}

// extractZip writes all files from r into destDir.
// Guards against zip-slip path traversal.
func extractZip(r *zip.ReadCloser, destDir string) error {
	base := filepath.Clean(destDir) + string(os.PathSeparator)

	for _, f := range r.File {
		target := filepath.Join(destDir, f.Name)

		// Zip-slip guard: target must stay inside destDir.
		if !strings.HasPrefix(filepath.Clean(target)+string(os.PathSeparator), base) {
			return fmt.Errorf("invalid path in zip: %s", f.Name)
		}

		if f.FileInfo().IsDir() {
			os.MkdirAll(target, 0755)
			continue
		}

		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}

		rc, err := f.Open()
		if err != nil {
			return err
		}

		out, err := os.Create(target)
		if err != nil {
			rc.Close()
			return err
		}

		_, err = io.Copy(out, rc)
		out.Close()
		rc.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// findFileByExt walks dir and returns the first file matching ext (case-insensitive).
func findFileByExt(dir, ext string) (string, error) {
	var found string
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && strings.EqualFold(filepath.Ext(path), ext) {
			found = path
			return filepath.SkipAll
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if found == "" {
		return "", fmt.Errorf("no %s file found", ext)
	}
	return found, nil
}
