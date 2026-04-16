// /*
// Copyright 2025 The Grove Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
// */

package utils

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/utils/ptr"
)

type CustomString string

type AnotherStringType string

func TestIsEmptyStringType(t *testing.T) {
	testCases := []struct {
		description string
		input       interface{}
		expected    bool
	}{
		{
			description: "empty string should return true",
			input:       "",
			expected:    true,
		},
		{
			description: "string with only spaces should return true",
			input:       "   ",
			expected:    true,
		},
		{
			description: "string with only tabs should return true",
			input:       "\t\t",
			expected:    true,
		},
		{
			description: "string with only newlines should return true",
			input:       "\n\n",
			expected:    true,
		},
		{
			description: "string with mixed whitespace should return true",
			input:       " \t\n ",
			expected:    true,
		},
		{
			description: "string with actual content should return false",
			input:       "hello",
			expected:    false,
		},
		{
			description: "string with content and leading/trailing spaces should return false",
			input:       "  hello  ",
			expected:    false,
		},
		{
			description: "string with content and mixed whitespace should return false",
			input:       "\t hello \n",
			expected:    false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			switch v := tc.input.(type) {
			case string:
				assert.Equal(t, tc.expected, IsEmptyStringType(v))
			}
		})
	}
}

func TestIsEmptyStringTypeWithCustomTypes(t *testing.T) {
	testCases := []struct {
		description string
		input       interface{}
		expected    bool
	}{
		{
			description: "empty CustomString should return true",
			input:       CustomString(""),
			expected:    true,
		},
		{
			description: "CustomString with spaces should return true",
			input:       CustomString("   "),
			expected:    true,
		},
		{
			description: "CustomString with content should return false",
			input:       CustomString("test"),
			expected:    false,
		},
		{
			description: "empty AnotherStringType should return true",
			input:       AnotherStringType(""),
			expected:    true,
		},
		{
			description: "AnotherStringType with tabs should return true",
			input:       AnotherStringType("\t\t"),
			expected:    true,
		},
		{
			description: "AnotherStringType with content should return false",
			input:       AnotherStringType("content"),
			expected:    false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			switch v := tc.input.(type) {
			case CustomString:
				assert.Equal(t, tc.expected, IsEmptyStringType(v))
			case AnotherStringType:
				assert.Equal(t, tc.expected, IsEmptyStringType(v))
			}
		})
	}
}

func TestOnlyOneIsNil(t *testing.T) {
	testCases := []struct {
		description string
		objA        interface{}
		objB        interface{}
		expected    bool
	}{
		{
			description: "both nil should return false",
			objA:        (*string)(nil),
			objB:        (*string)(nil),
			expected:    false,
		},
		{
			description: "first nil, second non-nil should return true",
			objA:        (*string)(nil),
			objB:        ptr.To("test"),
			expected:    true,
		},
		{
			description: "first non-nil, second nil should return true",
			objA:        ptr.To("test"),
			objB:        (*string)(nil),
			expected:    true,
		},
		{
			description: "both non-nil should return false",
			objA:        ptr.To("test1"),
			objB:        ptr.To("test2"),
			expected:    false,
		},
		{
			description: "both non-nil with same value should return false",
			objA:        ptr.To("same"),
			objB:        ptr.To("same"),
			expected:    false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			result := OnlyOneIsNil(tc.objA.(*string), tc.objB.(*string))
			assert.Equal(t, tc.expected, result)
		})
	}
}

func TestOnlyOneIsNilWithDifferentTypes(t *testing.T) {
	testCases := []struct {
		description string
		testFunc    func() bool
		expected    bool
	}{
		{
			description: "int pointers - both nil should return false",
			testFunc: func() bool {
				return OnlyOneIsNil((*int)(nil), (*int)(nil))
			},
			expected: false,
		},
		{
			description: "int pointers - first nil should return true",
			testFunc: func() bool {
				return OnlyOneIsNil((*int)(nil), ptr.To(42))
			},
			expected: true,
		},
		{
			description: "int pointers - second nil should return true",
			testFunc: func() bool {
				return OnlyOneIsNil(ptr.To(42), (*int)(nil))
			},
			expected: true,
		},
		{
			description: "int pointers - both non-nil should return false",
			testFunc: func() bool {
				return OnlyOneIsNil(ptr.To(42), ptr.To(84))
			},
			expected: false,
		},
		{
			description: "struct pointers - both non-nil should return false",
			testFunc: func() bool {
				type TestStruct struct{ _ string }
				return OnlyOneIsNil(&TestStruct{}, &TestStruct{})
			},
			expected: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			result := tc.testFunc()
			assert.Equal(t, tc.expected, result)
		})
	}
}

func TestOnlyOneIsEmpty(t *testing.T) {
	testCases := []struct {
		description string
		objA        []string
		objB        []string
		expected    bool
	}{
		{
			description: "both empty should return false",
			objA:        []string{},
			objB:        []string{},
			expected:    false,
		},
		{
			description: "first empty, second non-empty should return true",
			objA:        []string{},
			objB:        []string{"test"},
			expected:    true,
		},
		{
			description: "first non-empty, second empty should return true",
			objA:        []string{"test"},
			objB:        []string{},
			expected:    true,
		},
		{
			description: "both non-empty should return false",
			objA:        []string{"test1"},
			objB:        []string{"test2"},
			expected:    false,
		},
		{
			description: "nil and empty should return false",
			objA:        nil,
			objB:        []string{},
			expected:    false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			result := OnlyOneIsEmpty(tc.objA, tc.objB)
			assert.Equal(t, tc.expected, result)
		})
	}
}
