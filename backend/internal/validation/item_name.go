package validation

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

const maxItemNameRunes = 1024

var ErrInvalidItemName = errors.New("invalid item name")

var reservedItemNames = map[string]struct{}{
	".keep":     {},
	".objects":  {},
	".previews": {},
	".uploads":  {},
	".trash":    {},
}

func ItemName(name string) error {
	if !utf8.ValidString(name) {
		return fmt.Errorf("%w: name must be valid UTF-8", ErrInvalidItemName)
	}
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("%w: name is required", ErrInvalidItemName)
	}
	if name == "." || name == ".." {
		return fmt.Errorf("%w: reserved path segment", ErrInvalidItemName)
	}
	if _, reserved := reservedItemNames[strings.ToLower(name)]; reserved {
		return fmt.Errorf("%w: reserved internal name", ErrInvalidItemName)
	}
	if utf8.RuneCountInString(name) > maxItemNameRunes {
		return fmt.Errorf("%w: name exceeds %d characters", ErrInvalidItemName, maxItemNameRunes)
	}
	for _, character := range name {
		if character == '/' || character == '\\' {
			return fmt.Errorf("%w: name cannot contain path separators", ErrInvalidItemName)
		}
		if unicode.IsControl(character) {
			return fmt.Errorf("%w: name cannot contain control characters", ErrInvalidItemName)
		}
	}
	return nil
}

func ItemPath(itemPath string, allowEmpty bool) error {
	if !utf8.ValidString(itemPath) {
		return fmt.Errorf("%w: path must be valid UTF-8", ErrInvalidItemName)
	}
	if itemPath == "" && allowEmpty {
		return nil
	}
	if strings.TrimSpace(itemPath) == "" {
		return fmt.Errorf("%w: path is required", ErrInvalidItemName)
	}
	if strings.HasPrefix(itemPath, "/") || strings.HasSuffix(itemPath, "/") || strings.Contains(itemPath, "\\") {
		return fmt.Errorf("%w: path must contain relative item segments", ErrInvalidItemName)
	}
	for _, segment := range strings.Split(itemPath, "/") {
		if err := ItemName(segment); err != nil {
			return err
		}
	}
	return nil
}
