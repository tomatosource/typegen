package main

import (
	"strings"
)

type ListyError struct {
	Errors []error
}

func (le *ListyError) Error() string {
	if le == nil {
		return ""
	}

	var errorStrings []string
	for _, err := range []error(le.Errors) {
		errorStrings = append(errorStrings, err.Error())
	}
	return strings.Join(errorStrings, "\n")
}

func (le *ListyError) Add(err error) {
	if err == nil {
		return
	}

	if le == nil {
		le = &ListyError{
			Errors: []error{err},
		}
	} else {
		le.Errors = append(le.Errors, err)
	}
}

func errFromClosedChan(errs <-chan error) error {
	var errList *ListyError
	for err := range errs {
		errList.Add(err)
	}
	if errList == nil {
		return nil
	}
	return errList
}
