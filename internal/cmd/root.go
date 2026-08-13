package cmd

import (
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var (
	cfgFile string
	logger  *slog.Logger
)

var rootCmd = &cobra.Command{
	Use:   "watercolormap",
	Short: "A watercolor-styled map tile generator",
	Long: `WaterColorMap generates beautiful watercolor-styled map tiles from OpenStreetMap data.

It fetches OSM data for specific tiles, applies watercolor rendering techniques,
and outputs styled map tiles suitable for web mapping applications.`,
}

func Execute() {
	if logger == nil {
		initLogging() // fallback in case cobra init didn't fire
	}
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func init() {
	cobra.OnInitialize(initConfig, initLogging)

	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default is ./config.yaml)")
	rootCmd.PersistentFlags().String("data-source", "overpass", "Data source for OSM data (overpass, protomaps)")
	rootCmd.PersistentFlags().String("output-dir", "./tiles", "Output directory for generated tiles")
	rootCmd.PersistentFlags().Bool("verbose", false, "Enable verbose logging")
	rootCmd.PersistentFlags().String("log-level", "info", "Log level (debug, info, warn, error)")

	if err := viper.BindPFlag("data-source", rootCmd.PersistentFlags().Lookup("data-source")); err != nil {
		panic(fmt.Sprintf("failed to bind flag: %v", err))
	}
	if err := viper.BindPFlag("output-dir", rootCmd.PersistentFlags().Lookup("output-dir")); err != nil {
		panic(fmt.Sprintf("failed to bind flag: %v", err))
	}
	if err := viper.BindPFlag("verbose", rootCmd.PersistentFlags().Lookup("verbose")); err != nil {
		panic(fmt.Sprintf("failed to bind flag: %v", err))
	}
	if err := viper.BindPFlag("log-level", rootCmd.PersistentFlags().Lookup("log-level")); err != nil {
		panic(fmt.Sprintf("failed to bind flag: %v", err))
	}
}

func initConfig() {
	if cfgFile != "" {
		viper.SetConfigFile(cfgFile)
	} else {
		viper.AddConfigPath(".")
		viper.SetConfigType("yaml")
		viper.SetConfigName("config")
	}

	viper.SetEnvPrefix("WATERCOLORMAP")
	viper.AutomaticEnv()

	if err := viper.ReadInConfig(); err == nil {
		if viper.GetBool("verbose") {
			fmt.Fprintln(os.Stderr, "Using config file:", viper.ConfigFileUsed())
		}
	}
}

func initLogging() {
	levelStr := strings.ToLower(viper.GetString("log-level"))
	var level slog.Level
	switch levelStr {
	case "debug":
		level = slog.LevelDebug
	case "info", "":
		level = slog.LevelInfo
	case "warn", "warning":
		level = slog.LevelWarn
	case "error", "err":
		level = slog.LevelError
	default:
		fmt.Fprintf(os.Stderr, "Unknown log level %q, defaulting to info\n", levelStr)
		level = slog.LevelInfo
	}

	handler := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})
	logger = slog.New(handler)
	slog.SetDefault(logger)
}
