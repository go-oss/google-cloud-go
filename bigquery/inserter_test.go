// Copyright 2015 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package bigquery

import (
	"errors"
	"fmt"
	"strconv"
	"testing"

	"cloud.google.com/go/internal/pretty"
	"cloud.google.com/go/internal/testutil"
	"github.com/google/go-cmp/cmp"
	bq "google.golang.org/api/bigquery/v2"
)

type testSaver struct {
	row      map[string]Value
	insertID string
	err      error
}

func (ts testSaver) Save() (map[string]Value, string, error) {
	return ts.row, ts.insertID, ts.err
}

func TestNewInsertRequest(t *testing.T) {
	prev := randomIDFn
	n := 0
	randomIDFn = func() string { n++; return strconv.Itoa(n) }
	defer func() { randomIDFn = prev }()

	tests := []struct {
		ul     *Uploader
		savers []ValueSaver
		req    *bq.TableDataInsertAllRequest
	}{
		{
			ul:     &Uploader{},
			savers: nil,
			req:    nil,
		},
		{
			ul: &Uploader{},
			savers: []ValueSaver{
				testSaver{row: map[string]Value{"one": 1}},
				testSaver{row: map[string]Value{"two": 2}},
				testSaver{insertID: NoDedupeID, row: map[string]Value{"three": 3}},
			},
			req: &bq.TableDataInsertAllRequest{
				Rows: []*bq.TableDataInsertAllRequestRows{
					{InsertId: "1", Json: map[string]bq.JsonValue{"one": 1}},
					{InsertId: "2", Json: map[string]bq.JsonValue{"two": 2}},
					{InsertId: "", Json: map[string]bq.JsonValue{"three": 3}},
				},
			},
		},
		{
			ul: &Uploader{
				TableTemplateSuffix: "suffix",
				IgnoreUnknownValues: true,
				SkipInvalidRows:     true,
			},
			savers: []ValueSaver{
				testSaver{insertID: "a", row: map[string]Value{"one": 1}},
				testSaver{insertID: "", row: map[string]Value{"two": 2}},
			},
			req: &bq.TableDataInsertAllRequest{
				Rows: []*bq.TableDataInsertAllRequestRows{
					{InsertId: "a", Json: map[string]bq.JsonValue{"one": 1}},
					{InsertId: "3", Json: map[string]bq.JsonValue{"two": 2}},
				},
				TemplateSuffix:      "suffix",
				SkipInvalidRows:     true,
				IgnoreUnknownValues: true,
			},
		},
	}
	for i, tc := range tests {
		got, err := tc.ul.newInsertRequest(tc.savers)
		if err != nil {
			t.Fatal(err)
		}
		want := tc.req
		if !testutil.Equal(got, want) {
			t.Errorf("%d: %#v: got %#v, want %#v", i, tc.ul, got, want)
		}
	}
}

func TestNewInsertRequestErrors(t *testing.T) {
	var u Uploader
	_, err := u.newInsertRequest([]ValueSaver{testSaver{err: errors.New("bang")}})
	if err == nil {
		t.Error("got nil, want error")
	}
}

func TestHandleInsertErrors(t *testing.T) {
	rows := []*bq.TableDataInsertAllRequestRows{
		{InsertId: "a"},
		{InsertId: "b"},
	}
	for _, test := range []struct {
		description string
		in          []*bq.TableDataInsertAllResponseInsertErrors
		want        error
	}{
		{
			description: "nil error",
			in:          nil,
			want:        nil,
		},
		{
			description: "single error last row",
			in:          []*bq.TableDataInsertAllResponseInsertErrors{{Index: 1}},
			want:        PutMultiError{RowInsertionError{InsertID: "b", RowIndex: 1}},
		},
		{
			description: "single error first row",
			in:          []*bq.TableDataInsertAllResponseInsertErrors{{Index: 0}},
			want:        PutMultiError{RowInsertionError{InsertID: "a", RowIndex: 0}},
		},
		{
			description: "errors with extended message",
			in: []*bq.TableDataInsertAllResponseInsertErrors{
				{Errors: []*bq.ErrorProto{{Message: "m0"}}, Index: 0},
				{Errors: []*bq.ErrorProto{{Message: "m1"}}, Index: 1},
			},
			want: PutMultiError{
				RowInsertionError{InsertID: "a", RowIndex: 0, Errors: []error{&Error{Message: "m0"}}},
				RowInsertionError{InsertID: "b", RowIndex: 1, Errors: []error{&Error{Message: "m1"}}},
			},
		},
		{
			description: "invalid index",
			in: []*bq.TableDataInsertAllResponseInsertErrors{
				{Errors: []*bq.ErrorProto{{Message: "m0"}}, Index: 2},
			},
			want: fmt.Errorf("internal error: unexpected row index: 2"),
		},
	} {
		got := handleInsertErrors(test.in, rows)
		if _, ok := got.(PutMultiError); ok {
			// compare structure of the PutMultiError
			if !testutil.Equal(got, test.want) {
				t.Errorf("(case: %s)\nin %#v\ngot\n%#v\nwant\n%#v", test.description, test.in, got, test.want)
			}
			continue
		}

		if got != nil && test.want != nil && got.Error() != test.want.Error() {
			// check matching error messages
			t.Errorf("(case: %s)\nin %#v:\ngot\n%#v\nwant\n%#v", test.description, test.in, got, test.want)
		}

	}
}

func TestValueSavers(t *testing.T) {
	ts := &testSaver{}
	type T struct{ I int }
	schema, err := InferSchema(T{})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		in   interface{}
		want []ValueSaver
	}{
		{[]interface{}(nil), nil},
		{[]interface{}{}, nil},
		{ts, []ValueSaver{ts}},
		{T{I: 1}, []ValueSaver{&StructSaver{Schema: schema, Struct: T{I: 1}}}},
		{[]ValueSaver{ts, ts}, []ValueSaver{ts, ts}},
		{[]interface{}{ts, ts}, []ValueSaver{ts, ts}},
		{[]T{{I: 1}, {I: 2}}, []ValueSaver{
			&StructSaver{Schema: schema, Struct: T{I: 1}},
			&StructSaver{Schema: schema, Struct: T{I: 2}},
		}},
		{[]interface{}{T{I: 1}, &T{I: 2}}, []ValueSaver{
			&StructSaver{Schema: schema, Struct: T{I: 1}},
			&StructSaver{Schema: schema, Struct: &T{I: 2}},
		}},
		{&StructSaver{Struct: T{I: 3}, InsertID: "foo"},
			[]ValueSaver{
				&StructSaver{Schema: schema, Struct: T{I: 3}, InsertID: "foo"},
			}},
	} {
		got, err := valueSavers(test.in)
		if err != nil {
			t.Fatal(err)
		}
		if !testutil.Equal(got, test.want, cmp.AllowUnexported(testSaver{})) {
			t.Errorf("%+v: got %v, want %v", test.in, pretty.Value(got), pretty.Value(test.want))
		}
		// Make sure Save is successful.
		for i, vs := range got {
			_, _, err := vs.Save()
			if err != nil {
				t.Fatalf("%+v, #%d: got error %v, want nil", test.in, i, err)
			}
		}
	}
}

func TestValueSaversErrors(t *testing.T) {
	inputs := []interface{}{
		nil,
		1,
		[]int{1, 2},
		[]interface{}{
			testSaver{row: map[string]Value{"one": 1}, insertID: "a"},
			1,
		},
		StructSaver{},
	}
	for _, in := range inputs {
		if _, err := valueSavers(in); err == nil {
			t.Errorf("%#v: got nil, want error", in)
		}
	}
}
