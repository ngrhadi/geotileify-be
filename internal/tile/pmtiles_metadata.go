package tile

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"
)

// MetadataFields are the human-readable PMTiles JSON metadata fields exposed by
// the extension popup. Empty values remove the field (equivalent to null).
type MetadataFields struct {
	Name        string
	Description string
	Attribution string
}

const pmtilesHeaderSize = 127

// SetPMTilesMetadata merges name/description/attribution into the PMTiles v3
// JSON metadata section and rewrites the archive offsets. Tylertoo 0.8.0 does
// not expose these flags on the tiles command, so the backend injects them
// after tiling.
func SetPMTilesMetadata(path string, fields MetadataFields) error {
	if fields.Name == "" && fields.Description == "" && fields.Attribution == "" {
		return nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read pmtiles metadata: %w", err)
	}
	if len(data) < pmtilesHeaderSize || string(data[0:7]) != "PMTiles" || data[7] != 3 {
		return fmt.Errorf("invalid PMTiles v3 header")
	}

	rootOffset := binary.LittleEndian.Uint64(data[8:16])
	rootLength := binary.LittleEndian.Uint64(data[16:24])
	jsonOffset := binary.LittleEndian.Uint64(data[24:32])
	jsonLength := binary.LittleEndian.Uint64(data[32:40])
	leafOffset := binary.LittleEndian.Uint64(data[40:48])
	leafLength := binary.LittleEndian.Uint64(data[48:56])
	tileOffset := binary.LittleEndian.Uint64(data[56:64])
	tileLength := binary.LittleEndian.Uint64(data[64:72])
	internalCompression := data[97]

	fileLen := uint64(len(data))
	if rootOffset < pmtilesHeaderSize || rootOffset+rootLength > fileLen ||
		jsonOffset+jsonLength > fileLen ||
		leafOffset+leafLength > fileLen ||
		tileOffset+tileLength > fileLen {
		return fmt.Errorf("PMTiles section offsets are out of range")
	}

	var existing []byte
	if jsonLength > 0 {
		existing, err = decompressMetadata(data[jsonOffset:jsonOffset+jsonLength], internalCompression)
		if err != nil {
			return err
		}
	}

	meta := map[string]any{}
	if len(bytes.TrimSpace(existing)) > 0 {
		if err := json.Unmarshal(existing, &meta); err != nil {
			return fmt.Errorf("parse PMTiles JSON metadata: %w", err)
		}
	}

	if fields.Name != "" {
		meta["name"] = fields.Name
	} else {
		delete(meta, "name")
	}
	if fields.Description != "" {
		meta["description"] = fields.Description
	} else {
		delete(meta, "description")
	}
	if fields.Attribution != "" {
		meta["attribution"] = fields.Attribution
	} else {
		delete(meta, "attribution")
	}

	raw, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("encode PMTiles JSON metadata: %w", err)
	}
	newJSON, err := compressMetadata(raw, internalCompression)
	if err != nil {
		return err
	}

	rootEnd := int(rootOffset + rootLength)
	newJSONOffset := rootEnd
	newLeafOffset := newJSONOffset + len(newJSON)
	newTileOffset := newLeafOffset + int(leafLength)
	newLen := newTileOffset + int(tileLength)

	out := make([]byte, newLen)
	copy(out, data[:rootEnd])
	copy(out[newJSONOffset:], newJSON)
	if leafLength > 0 {
		copy(out[newLeafOffset:], data[leafOffset:leafOffset+leafLength])
	}
	if tileLength > 0 {
		copy(out[newTileOffset:], data[tileOffset:tileOffset+tileLength])
	}

	binary.LittleEndian.PutUint64(out[24:32], uint64(newJSONOffset))
	binary.LittleEndian.PutUint64(out[32:40], uint64(len(newJSON)))
	binary.LittleEndian.PutUint64(out[40:48], uint64(newLeafOffset))
	binary.LittleEndian.PutUint64(out[56:64], uint64(newTileOffset))

	tmp := path + ".metadata.tmp"
	if err := os.WriteFile(tmp, out, 0o644); err != nil {
		return fmt.Errorf("write PMTiles metadata temp file: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("replace PMTiles metadata: %w", err)
	}
	return nil
}

func decompressMetadata(data []byte, method byte) ([]byte, error) {
	switch method {
	case 1: // none
		return data, nil
	case 2: // gzip
		zr, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("open PMTiles metadata gzip: %w", err)
		}
		defer zr.Close()
		out, err := io.ReadAll(zr)
		if err != nil {
			return nil, fmt.Errorf("decompress PMTiles metadata: %w", err)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("unsupported PMTiles internal compression %d", method)
	}
}

func compressMetadata(data []byte, method byte) ([]byte, error) {
	switch method {
	case 1: // none
		return data, nil
	case 2: // gzip
		var buf bytes.Buffer
		zw := gzip.NewWriter(&buf)
		if _, err := zw.Write(data); err != nil {
			zw.Close()
			return nil, fmt.Errorf("compress PMTiles metadata: %w", err)
		}
		if err := zw.Close(); err != nil {
			return nil, fmt.Errorf("close PMTiles metadata gzip: %w", err)
		}
		return buf.Bytes(), nil
	default:
		return nil, fmt.Errorf("unsupported PMTiles internal compression %d", method)
	}
}
