package jsoncfg

import (
	"encoding/json"
	"fmt"
	"os"
)

// Load reads a JSON config file into target.
func Load(path string, target any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read config file %s failed: %w", path, err)
	}
	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("parse config file %s failed: %w", path, err)
	}
	return nil
}
