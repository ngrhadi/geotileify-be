package tile

import (
	"os"
	"os/exec"
)

func GeneratePMTiles(input, output string) error {
    cmd := exec.Command("tippecanoe",
        "-o", output,
        "-zg",
        "--force",
		"--generate-ids",
        input,
    )
    cmd.Stdout = os.Stdout
    cmd.Stderr = os.Stderr
    return cmd.Run()
}

func ConvertParquetToGeoJSON(input, output string) error {
    cmd := exec.Command(
        "ogr2ogr",
        "-f", "GeoJSON",
        output,
        input,
    )
    cmd.Stdout = os.Stdout
    cmd.Stderr = os.Stderr
    return cmd.Run()
}
