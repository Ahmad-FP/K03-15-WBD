package authorization

import "errors"

type Level string

const (
	None     Level = "none"
	ViewOnly Level = "view_only"
	ViewEdit Level = "view_edit"
)

var ErrInvalidLevel = errors.New("invalid database access level")

func Effective(levels []Level) Level {
	best := None
	for _, level := range levels {
		switch level {
		case ViewEdit:
			return ViewEdit
		case ViewOnly:
			best = ViewOnly
		}
	}
	return best
}

func ValidateLevel(level Level) error {
	if level != ViewOnly && level != ViewEdit {
		return ErrInvalidLevel
	}
	return nil
}

func Allows(actual, required Level) bool {
	if required == None {
		return true
	}
	if actual == ViewEdit {
		return required == ViewOnly || required == ViewEdit
	}
	return actual == ViewOnly && required == ViewOnly
}
