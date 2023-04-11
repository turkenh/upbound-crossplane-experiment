/*
Copyright 2023 The Crossplane Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1

import (
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/utils/pointer"

	xperrors "github.com/crossplane/crossplane-runtime/pkg/errors"
	"github.com/crossplane/crossplane-runtime/pkg/test"

	"github.com/crossplane/crossplane/pkg/validation/schema"
)

func TestTransform_Validate(t *testing.T) {
	type args struct {
		transform *Transform
	}
	type want struct {
		err *field.Error
	}

	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"ValidMath": {
			reason: "Math transform with MathTransform set should be valid",
			args: args{
				transform: &Transform{
					Type: TransformTypeMath,
					Math: &MathTransform{
						Multiply: pointer.Int64(2),
					},
				},
			},
		},
		"InvalidMath": {
			reason: "Math transform with no MathTransform set should be invalid",
			args: args{
				transform: &Transform{
					Type: TransformTypeMath,
					Math: nil,
				},
			},
			want: want{
				&field.Error{
					Type:  field.ErrorTypeRequired,
					Field: "math",
				},
			},
		},
		"ValidMap": {
			reason: "Map transform with MapTransform set should be valid",
			args: args{
				transform: &Transform{
					Type: TransformTypeMap,
					Map: &MapTransform{
						Pairs: map[string]extv1.JSON{
							"foo": {Raw: []byte(`"bar"`)},
						},
					},
				},
			},
		},
		"InvalidMapNoMap": {
			reason: "Map transform with no map set should be invalid",
			args: args{
				transform: &Transform{
					Type: TransformTypeMap,
					Map:  nil,
				},
			},
			want: want{
				err: &field.Error{
					Type:  field.ErrorTypeRequired,
					Field: "map",
				},
			},
		},
		"InvalidMapNoPairs": {
			reason: "Map transform with no pairs in map should be invalid",
			args: args{
				transform: &Transform{
					Type: TransformTypeMap,
					Map:  &MapTransform{},
				},
			},
			want: want{
				err: &field.Error{
					Type:  field.ErrorTypeRequired,
					Field: "map.pairs",
				},
			},
		},
		"InvalidMatchNoMatch": {
			reason: "Match transform with no match set should be invalid",
			args: args{
				transform: &Transform{
					Type:  TransformTypeMatch,
					Match: nil,
				},
			},
			want: want{
				&field.Error{
					Type:  field.ErrorTypeRequired,
					Field: "match",
				},
			},
		},
		"InvalidMatchEmptyTransform": {
			reason: "Match transform with empty MatchTransform should be invalid",
			args: args{
				transform: &Transform{
					Type:  TransformTypeMatch,
					Match: &MatchTransform{},
				},
			},
			want: want{
				err: &field.Error{
					Type:  field.ErrorTypeRequired,
					Field: "match.patterns",
				},
			},
		},
		"ValidMatchTransformRegexp": {
			reason: "Match transform with valid MatchTransform of type regexp should be valid",
			args: args{
				transform: &Transform{
					Type: TransformTypeMatch,
					Match: &MatchTransform{
						Patterns: []MatchTransformPattern{
							{
								Type:   MatchTransformPatternTypeRegexp,
								Regexp: pointer.String(".*"),
							},
						},
					},
				},
			},
		},
		"InvalidMatchTransformRegexp": {
			reason: "Match transform with an invalid MatchTransform of type regexp with a bad regexp should be invalid",
			args: args{
				transform: &Transform{
					Type: TransformTypeMatch,
					Match: &MatchTransform{
						Patterns: []MatchTransformPattern{
							{
								Type:   MatchTransformPatternTypeRegexp,
								Regexp: pointer.String("?"),
							},
						},
					},
				},
			},
			want: want{
				err: &field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: "match.patterns[0].regexp",
				},
			},
		},
		"ValidMatchTransformString": {
			reason: "Match transform with valid MatchTransform of type literal should be valid",
			args: args{
				transform: &Transform{
					Type: TransformTypeMatch,
					Match: &MatchTransform{
						Patterns: []MatchTransformPattern{
							{
								Type:    MatchTransformPatternTypeLiteral,
								Literal: pointer.String("foo"),
							},
							{
								Literal: pointer.String("bar"),
							},
						},
					},
				},
			},
		},
		"InvalidStringNoString": {
			reason: "String transform with no string set should be invalid",
			args: args{
				transform: &Transform{
					Type:   TransformTypeString,
					String: nil,
				},
			},
			want: want{
				err: &field.Error{
					Type:  field.ErrorTypeRequired,
					Field: "string",
				},
			},
		},
		"ValidString": {
			reason: "String transform with set string should be valid",
			args: args{
				transform: &Transform{
					Type: TransformTypeString,
					String: &StringTransform{
						Format: pointer.String("foo"),
					},
				},
			},
		},
		"InvalidConvertMissingConvert": {
			reason: "Convert transform missing Convert should be invalid",
			args: args{
				transform: &Transform{
					Type:    TransformTypeConvert,
					Convert: nil,
				},
			},
			want: want{
				err: &field.Error{
					Type:  field.ErrorTypeRequired,
					Field: "convert",
				},
			},
		},
		"InvalidConvertUnknownFormat": {
			reason: "Convert transform with unknown format should be invalid",
			args: args{
				transform: &Transform{
					Type: TransformTypeConvert,
					Convert: &ConvertTransform{
						Format: &[]ConvertTransformFormat{"foo"}[0],
					},
				},
			},
			want: want{
				err: &field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: "convert.format",
				},
			},
		},
		"InvalidConvertUnknownToType": {
			reason: "Convert transform with unknown toType should be invalid",
			args: args{
				transform: &Transform{
					Type: TransformTypeConvert,
					Convert: &ConvertTransform{
						ToType: TransformIOType("foo"),
					},
				},
			},
			want: want{
				err: &field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: "convert.toType",
				},
			},
		},
		"ValidConvert": {
			reason: "Convert transform with valid format and toType should be valid",
			args: args{
				transform: &Transform{
					Type: TransformTypeConvert,
					Convert: &ConvertTransform{
						Format: &[]ConvertTransformFormat{ConvertTransformFormatNone}[0],
						ToType: TransformIOTypeInt,
					},
				},
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			err := tc.args.transform.Validate()
			if diff := cmp.Diff(tc.want.err, err, cmpopts.IgnoreFields(field.Error{}, "Detail", "BadValue")); diff != "" {
				t.Errorf("%s\nValidate(...): -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
}

func TestConvertTransform_GetConversionFunc(t *testing.T) {
	type args struct {
		ct   *ConvertTransform
		from TransformIOType
	}
	type want struct {
		err error
	}
	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"IntToString": {
			reason: "Int to String should be valid",
			args: args{
				ct: &ConvertTransform{
					ToType: TransformIOTypeString,
				},
				from: TransformIOTypeInt,
			},
		},
		"IntToInt": {
			reason: "Int to Int should be valid",
			args: args{
				ct: &ConvertTransform{
					ToType: TransformIOTypeInt,
				},
				from: TransformIOTypeInt,
			},
		},
		"IntToInt64": {
			reason: "Int to Int64 should be valid",
			args: args{
				ct: &ConvertTransform{
					ToType: TransformIOTypeInt,
				},
				from: TransformIOTypeInt64,
			},
		},
		"Int64ToInt": {
			reason: "Int64 to Int should be valid",
			args: args{
				ct: &ConvertTransform{
					ToType: TransformIOTypeInt64,
				},
				from: TransformIOTypeInt,
			},
		},
		"IntToFloat": {
			reason: "Int to Float should be valid",
			args: args{
				ct: &ConvertTransform{
					ToType: TransformIOTypeInt,
				},
				from: TransformIOTypeFloat64,
			},
		},
		"IntToBool": {
			reason: "Int to Bool should be valid",
			args: args{
				ct: &ConvertTransform{
					ToType: TransformIOTypeInt,
				},
				from: TransformIOTypeBool,
			},
		},
		"StringToIntInvalidFormat": {
			reason: "String to Int with invalid format should be invalid",
			args: args{
				ct: &ConvertTransform{
					ToType: TransformIOTypeInt,
					Format: &[]ConvertTransformFormat{"wrong"}[0],
				},
				from: TransformIOTypeString,
			},
			want: want{
				err: fmt.Errorf("conversion from string to int64 is not supported with format wrong"),
			},
		},
		"IntToIntInvalidFormat": {
			reason: "Int to Int, invalid format ignored because it is the same type",
			args: args{
				ct: &ConvertTransform{
					ToType: TransformIOTypeInt,
					Format: &[]ConvertTransformFormat{"wrong"}[0],
				},
				from: TransformIOTypeInt,
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := tc.args.ct.GetConversionFunc(tc.args.from)
			if diff := cmp.Diff(tc.want.err, err, test.EquateErrors()); diff != "" {
				t.Errorf("%s\nGetConversionFunc(...): -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
}

func TestConvertTransformType_ToKnownJSONType(t *testing.T) {
	type args struct {
		c TransformIOType
	}
	type want struct {
		t schema.KnownJSONType
	}
	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"Int": {
			reason: "Int",
			args: args{
				c: TransformIOTypeInt,
			},
			want: want{
				t: schema.KnownJSONTypeInteger,
			},
		},
		"Int64": {
			reason: "Int64",
			args: args{
				c: TransformIOTypeInt64,
			},
			want: want{
				t: schema.KnownJSONTypeInteger,
			},
		},
		"Float64": {
			reason: "Float64",
			args: args{
				c: TransformIOTypeFloat64,
			},
			want: want{
				t: schema.KnownJSONTypeNumber,
			},
		},
		"Unknown": {
			reason: "Unknown returns empty string, should never happen",
			args: args{
				c: TransformIOType("foo"),
			},
			want: want{
				t: "",
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := tc.args.c.ToKnownJSONType()
			if diff := cmp.Diff(tc.want.t, got); diff != "" {
				t.Errorf("\n%s\nToKnownJSONType(...): -want error, +got error:\n%s", tc.reason, diff)
			}
			if tc.want.t == "" && tc.args.c.IsValid() {
				t.Errorf("IsValid() should return false for unknown type: %s", tc.args.c)
			}
		})
	}
}

func TestFromKnownJSONType(t *testing.T) {
	type args struct {
		t schema.KnownJSONType
	}
	type want struct {
		out TransformIOType
		err error
	}
	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"ValidInt": {
			reason: "Int should be valid and convert properly",
			args: args{
				t: schema.KnownJSONTypeInteger,
			},
			want: want{
				out: TransformIOTypeInt64,
			},
		},
		"ValidNumber": {
			reason: "Number should be valid and convert properly",
			args: args{
				t: schema.KnownJSONTypeNumber,
			},
			want: want{
				out: TransformIOTypeFloat64,
			},
		},
		"InvalidUnknown": {
			reason: "Unknown return an error",
			args: args{
				t: schema.KnownJSONType("foo"),
			},
			want: want{
				err: xperrors.Errorf(errFmtUnknownJSONType, "foo"),
			},
		},
		"InvalidEmpty": {
			reason: "Empty string return an error",
			args: args{
				t: "",
			},
			want: want{
				err: xperrors.Errorf(errFmtUnknownJSONType, ""),
			},
		},
		"InvalidNull": {
			reason: "Null return an error",
			args: args{
				t: schema.KnownJSONTypeNull,
			},
			want: want{
				err: xperrors.Errorf(errFmtUnsupportedJSONType, schema.KnownJSONTypeNull),
			},
		},
		"ValidBoolean": {
			reason: "Boolean should be valid and convert properly",
			args: args{
				t: schema.KnownJSONTypeBoolean,
			},
			want: want{
				out: TransformIOTypeBool,
			},
		},
		"InvalidArray": {
			reason: "Array should not be valid",
			args:   args{t: schema.KnownJSONTypeArray},
			want: want{
				err: xperrors.Errorf(errFmtUnsupportedJSONType, schema.KnownJSONTypeArray),
			},
		},
		"InvalidObject": {
			reason: "Object should not be valid",
			args:   args{t: schema.KnownJSONTypeObject},
			want: want{
				err: xperrors.Errorf(errFmtUnsupportedJSONType, schema.KnownJSONTypeObject),
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := FromKnownJSONType(tc.args.t)
			if diff := cmp.Diff(tc.want.err, err, test.EquateErrors()); diff != "" {
				t.Errorf("\n%s\nFromKnownJSONType(...): -want error, +got error:\n%s", tc.reason, diff)
				return
			}
			if diff := cmp.Diff(tc.want.out, got); diff != "" {
				t.Errorf("\n%s\nFromKnownJSONType(...): -want error, +got error:\n%s", tc.reason, diff)
			}
		})
	}
}

func TestTransform_GetOutputType(t *testing.T) {
	type args struct {
		transform *Transform
	}
	type want struct {
		output *TransformIOType
		err    error
	}
	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"MapTransform": {
			reason: "Output of Math transform should be float64",
			args: args{
				transform: &Transform{
					Type: TransformTypeMath,
				},
			},
			want: want{
				output: &[]TransformIOType{TransformIOTypeFloat64}[0],
			},
		},
		"ConvertTransform": {
			reason: "Output of Convert transform, no validation, should be the type specified",
			args: args{
				transform: &Transform{
					Type:    TransformTypeConvert,
					Convert: &ConvertTransform{ToType: "fakeType"},
				},
			},
			want: want{
				output: &[]TransformIOType{"fakeType"}[0],
			},
		},
		"ErrorUnknownType": {
			reason: "Output of Unknown transform type returns an error",
			args: args{
				transform: &Transform{
					Type: "fakeType",
				},
			},
			want: want{
				err: fmt.Errorf("unable to get output type, unknown transform type: fakeType"),
			},
		},
		"MapTransformNil": {
			reason: "Output of Map transform is nil",
			args: args{
				transform: &Transform{
					Type: TransformTypeMap,
				},
			},
		},
		"MatchTransformNil": {
			reason: "Output of Match transform is nil",
			args: args{
				transform: &Transform{
					Type: TransformTypeMatch,
				},
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := tc.args.transform.GetOutputType()
			if diff := cmp.Diff(tc.want.err, err, test.EquateErrors()); diff != "" {
				t.Errorf("%s\nGetOutputType(...): -want, +got:\n%s", tc.reason, diff)
			}

			if diff := cmp.Diff(tc.want.output, got); diff != "" {
				t.Errorf("%s\nGetOutputType(...): -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
}
