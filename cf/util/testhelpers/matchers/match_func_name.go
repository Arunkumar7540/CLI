package matchers

import (
	"code.cloudfoundry.org/cli/actor/v7pushaction"
	"fmt"
	"github.com/onsi/gomega"
	"reflect"
	"runtime"
)

type ChangeAppFuncsByNameMatcher struct {
	expected    []string
	actualNames []string
}

func MatchChangeAppFuncsByName(funcs ...v7pushaction.ChangeApplicationFunc) gomega.OmegaMatcher {
	var names []string
	for _, fn := range funcs {
		// just to be safe
		if reflect.TypeOf(fn).Kind() == reflect.Func {
			name := runtime.FuncForPC(reflect.ValueOf(fn).Pointer()).Name()
			names = append(names, name)
		}
	}
	return &ChangeAppFuncsByNameMatcher{expected: names}
}

func (matcher *ChangeAppFuncsByNameMatcher) Match(actual interface{}) (success bool, err error) {
	if reflect.TypeOf(actual).Kind() != reflect.Slice {
		return false, fmt.Errorf("MatchChangeAppFuncsByName: Actual must be a slice of ChangeApplicationFuncs")
	}

	arr := reflect.ValueOf(actual)

	for i := 0; i < arr.Len(); i++ {
		elem := arr.Index(i)
		if elem.Kind() != reflect.Func {
			return false, fmt.Errorf("MatchChangeAppFuncsByName: Actual must be a slice of ChangeApplicationFuncs")
		}
		matcher.actualNames = append(matcher.actualNames, runtime.FuncForPC(elem.Pointer()).Name())
	}

	return reflect.DeepEqual(matcher.actualNames, matcher.expected), nil
}

func (matcher *ChangeAppFuncsByNameMatcher) FailureMessage(actual interface{}) string {
	return fmt.Sprintf("Expected:\n \"%v\"\n to match actual:\n\"%v\"\n", matcher.expected, matcher.actualNames)
}

func (matcher *ChangeAppFuncsByNameMatcher) NegatedFailureMessage(actual interface{}) string {
	return fmt.Sprintf("Expected:\n \"%v\"\n not to match\n actual:\n\"%v\"\n", matcher.expected, matcher.actualNames)
}
