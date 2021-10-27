package refopts

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/github/git-sizer/git"
	"github.com/github/git-sizer/sizes"
)

type filterValue struct {
	// rgb is the RefGroupBuilder whose top-level filter is
	// affected if this option is used.
	rgb *RefGroupBuilder

	// combiner specifies how the filter generated by this option
	// is combined with the existing filter; i.e., does it cause
	// the matching references to be included or excluded?
	combiner git.Combiner

	// pattern, if it is set, is the pattern (prefix or regexp) to
	// be matched. If it is not set, then the user must supply the
	// pattern.
	pattern string

	// regexp specifies whether `pattern` should be interpreted as
	// a regexp (as opposed to handling it flexibly).
	regexp bool
}

func (v *filterValue) Set(s string) error {
	var filter git.ReferenceFilter
	combiner := v.combiner

	var pattern string
	if v.pattern != "" {
		// The pattern is fixed for this option:
		pattern = v.pattern

		// It's not really expected, but if the user supplied a
		// `false` boolean value, invert the polarity:
		b, err := strconv.ParseBool(s)
		if err != nil {
			return err
		}
		if !b {
			combiner = combiner.Inverted()
		}
	} else {
		// The user must supply the pattern.
		pattern = s
	}

	if v.regexp {
		var err error
		filter, err = git.RegexpFilter(pattern)
		if err != nil {
			return fmt.Errorf("invalid regexp: %q", s)
		}
	} else {
		var err error
		filter, err = v.interpretFlexibly(pattern)
		if err != nil {
			return err
		}
	}

	v.rgb.topLevelGroup.filter = combiner.Combine(v.rgb.topLevelGroup.filter, filter)

	return nil
}

// Interpret an option argument flexibly:
//
// * If it is bracketed with `/` characters, treat it as a regexp.
//
// * If it starts with `@`, then consider it a refgroup name. That
//   refgroup must already be defined. Use its filter. This construct
//   is only allowed at the top level.
//
// * Otherwise treat it as a prefix.
func (v *filterValue) interpretFlexibly(s string) (git.ReferenceFilter, error) {
	if len(s) >= 2 && strings.HasPrefix(s, "/") && strings.HasSuffix(s, "/") {
		pattern := s[1 : len(s)-1]
		return git.RegexpFilter(pattern)
	}

	if len(s) >= 1 && s[0] == '@' {
		name := sizes.RefGroupSymbol(s[1:])
		if name == "" {
			return nil, errors.New("missing refgroup name")
		}

		refGroup := v.rgb.groups[name]
		if refGroup == nil {
			return nil, fmt.Errorf("undefined refgroup '%s'", name)
		}

		return refGroupFilter{refGroup}, nil
	}

	return git.PrefixFilter(s), nil
}

func (v *filterValue) Get() interface{} {
	return nil
}

func (v *filterValue) String() string {
	return ""
}

func (v *filterValue) Type() string {
	if v.pattern != "" {
		return "bool"
	} else if v.regexp {
		return "regexp"
	} else {
		return "prefix"
	}
}