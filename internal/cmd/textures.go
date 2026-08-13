package cmd

import (
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/cwbudde/watercolormap/internal/texture"
)

var texturesCmd = &cobra.Command{
	Use:   "textures",
	Short: "Generate seamless watercolor textures",
	Long:  "Generate the default set of seamless watercolor textures used by the renderer.",
	RunE:  runTextures,
}

func init() {
	rootCmd.AddCommand(texturesCmd)

	texturesCmd.Flags().String("textures-dir", filepath.Join("assets", "textures"), "Output directory for generated textures")
	texturesCmd.Flags().Int("size", 1024, "Texture size in pixels (square)")
	texturesCmd.Flags().Int64("seed", 1337, "Deterministic seed for texture generation")
	texturesCmd.Flags().Float64("variation", 1.0, "Global variation multiplier (0..1) applied to defaults")
	texturesCmd.Flags().Float64("brushness", 1.0, "Brush stroke strength (0..1)")
	texturesCmd.Flags().Bool("force", false, "Overwrite textures that already exist")

	bindFlags := []struct {
		key  string
		flag string
	}{
		{"textures.dir", "textures-dir"},
		{"textures.size", "size"},
		{"textures.seed", "seed"},
		{"textures.variation", "variation"},
		{"textures.brushness", "brushness"},
		{"textures.force", "force"},
	}

	for _, bf := range bindFlags {
		if err := viper.BindPFlag(bf.key, texturesCmd.Flags().Lookup(bf.flag)); err != nil {
			panic(fmt.Sprintf("failed to bind flag %s: %v", bf.flag, err))
		}
	}
}

func runTextures(cmd *cobra.Command, args []string) error {
	if logger == nil {
		initLogging()
	}

	dir := viper.GetString("textures.dir")
	size := viper.GetInt("textures.size")
	seed := viper.GetInt64("textures.seed")
	variation := viper.GetFloat64("textures.variation")
	brushness := viper.GetFloat64("textures.brushness")
	force := viper.GetBool("textures.force")

	if size <= 0 {
		return fmt.Errorf("size must be positive")
	}
	if variation < 0 || variation > 1 {
		return fmt.Errorf("variation must be within [0,1]")
	}
	if brushness < 0 || brushness > 1 {
		return fmt.Errorf("brushness must be within [0,1]")
	}

	result, err := texture.WriteDefaultTextures(dir, size, seed, variation, brushness, force)
	if err != nil {
		return err
	}

	logger.Info("Texture generation complete",
		"dir", dir,
		"written", len(result.Written),
		"skipped", len(result.Skipped),
	)
	return nil
}
