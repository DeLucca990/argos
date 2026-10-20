package dotenv

import (
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/joho/godotenv"
)

func LoadEnvVariables() {
	if err := Load(); err != nil {
		log.Printf("env file: %v — continuing without it", err)
	}
}

func Load() error {
	for _, dir := range searchDirs() {
		path := filepath.Join(dir, ".env")
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return loadFile(path)
		}
	}
	return nil
}

func searchDirs() []string {
	var dirs []string
	if exe, err := os.Executable(); err == nil {
		dirs = append(dirs, filepath.Dir(exe), filepath.Dir(filepath.Dir(exe)))
	}
	if wd, err := os.Getwd(); err == nil {
		dirs = append(dirs, wd)
	}
	return dirs
}

func loadFile(path string) error {
	if err := godotenv.Load(path); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}
