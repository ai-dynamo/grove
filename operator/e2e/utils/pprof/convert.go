// /*
// Copyright 2026 The Grove Authors.
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

package pprof

import (
	"bytes"
	"compress/gzip"
	"fmt"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"

	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
)

// profileMessageType is a lazily-built message type for the pprof Profile message.
// Built from the proto3 schema at https://github.com/google/pprof/blob/main/proto/profile.proto
// This avoids importing a generated package that doesn't exist in our dependency tree.
var profileMessageType = func() protoreflect.MessageType {
	fdp := &descriptorpb.FileDescriptorProto{
		Name:    strPtr("perftools/profiles/profile.proto"),
		Package: strPtr("perftools.profiles"),
		Syntax:  strPtr("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{
			{
				Name: strPtr("Profile"),
				Field: []*descriptorpb.FieldDescriptorProto{
					repeatedMsg(1, "sample_type", "ValueType"),
					repeatedMsg(2, "sample", "Sample"),
					repeatedMsg(3, "mapping", "Mapping"),
					repeatedMsg(4, "location", "Location"),
					repeatedMsg(5, "function", "Function"),
					repeatedStr(6, "string_table"),
					optionalInt64(7, "drop_frames"),
					optionalInt64(8, "keep_frames"),
					optionalInt64(9, "time_nanos"),
					optionalInt64(10, "duration_nanos"),
					optionalMsg(11, "period_type", "ValueType"),
					optionalInt64(12, "period"),
					repeatedInt64(13, "comment"),
					optionalInt64(14, "default_sample_type"),
				},
			},
			{
				Name: strPtr("ValueType"),
				Field: []*descriptorpb.FieldDescriptorProto{
					optionalInt64(1, "type"),
					optionalInt64(2, "unit"),
				},
			},
			{
				Name: strPtr("Sample"),
				Field: []*descriptorpb.FieldDescriptorProto{
					repeatedUint64(1, "location_id"),
					repeatedInt64(2, "value"),
					repeatedMsg(3, "label", "Label"),
				},
			},
			{
				Name: strPtr("Label"),
				Field: []*descriptorpb.FieldDescriptorProto{
					optionalInt64(1, "key"),
					optionalInt64(2, "str"),
					optionalInt64(3, "num"),
					optionalInt64(4, "num_unit"),
				},
			},
			{
				Name: strPtr("Mapping"),
				Field: []*descriptorpb.FieldDescriptorProto{
					optionalUint64(1, "id"),
					optionalUint64(2, "memory_start"),
					optionalUint64(3, "memory_limit"),
					optionalUint64(4, "file_offset"),
					optionalInt64(5, "filename"),
					optionalInt64(6, "build_id"),
					optionalBool(7, "has_functions"),
					optionalBool(8, "has_filenames"),
					optionalBool(9, "has_line_numbers"),
					optionalBool(10, "has_inline_frames"),
				},
			},
			{
				Name: strPtr("Location"),
				Field: []*descriptorpb.FieldDescriptorProto{
					optionalUint64(1, "id"),
					optionalUint64(2, "mapping_id"),
					optionalUint64(3, "address"),
					repeatedMsg(4, "line", "Line"),
					optionalBool(5, "is_folded"),
				},
			},
			{
				Name: strPtr("Line"),
				Field: []*descriptorpb.FieldDescriptorProto{
					optionalUint64(1, "function_id"),
					optionalInt64(2, "line"),
				},
			},
			{
				Name: strPtr("Function"),
				Field: []*descriptorpb.FieldDescriptorProto{
					optionalUint64(1, "id"),
					optionalInt64(2, "name"),
					optionalInt64(3, "system_name"),
					optionalInt64(4, "filename"),
					optionalInt64(5, "start_line"),
				},
			},
		},
	}
	fd, err := protodesc.NewFile(fdp, nil)
	if err != nil {
		panic(fmt.Sprintf("build profile proto descriptor: %v", err))
	}
	return dynamicpb.NewMessageType(fd.Messages().ByName("Profile"))
}()

// jsonProfileToGzipPprof converts a JSON-encoded google.perftools.profiles.Profile
// (as returned by Pyroscope's SelectMergeProfile Connect RPC endpoint) into
// gzip-compressed binary pprof format readable by `go tool pprof`.
func jsonProfileToGzipPprof(jsonData []byte) ([]byte, error) {
	msg := profileMessageType.New().Interface()
	if err := protojson.Unmarshal(jsonData, msg); err != nil {
		return nil, fmt.Errorf("unmarshal JSON profile: %w", err)
	}
	raw, err := proto.Marshal(msg)
	if err != nil {
		return nil, fmt.Errorf("marshal binary profile: %w", err)
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write(raw); err != nil {
		return nil, fmt.Errorf("gzip write: %w", err)
	}
	if err := gz.Close(); err != nil {
		return nil, fmt.Errorf("gzip close: %w", err)
	}
	return buf.Bytes(), nil
}

func strPtr(s string) *string { return &s }

func fieldDesc(num int32, name string, typ descriptorpb.FieldDescriptorProto_Type, label descriptorpb.FieldDescriptorProto_Label, typeName *string) *descriptorpb.FieldDescriptorProto {
	return &descriptorpb.FieldDescriptorProto{
		Name:     strPtr(name),
		Number:   int32Ptr(num),
		Type:     &typ,
		Label:    &label,
		TypeName: typeName,
	}
}

func int32Ptr(i int32) *int32 { return &i }

func optionalInt64(num int32, name string) *descriptorpb.FieldDescriptorProto {
	return fieldDesc(num, name, descriptorpb.FieldDescriptorProto_TYPE_INT64, descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL, nil)
}

func optionalUint64(num int32, name string) *descriptorpb.FieldDescriptorProto {
	return fieldDesc(num, name, descriptorpb.FieldDescriptorProto_TYPE_UINT64, descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL, nil)
}

func optionalBool(num int32, name string) *descriptorpb.FieldDescriptorProto {
	return fieldDesc(num, name, descriptorpb.FieldDescriptorProto_TYPE_BOOL, descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL, nil)
}

func repeatedInt64(num int32, name string) *descriptorpb.FieldDescriptorProto {
	return fieldDesc(num, name, descriptorpb.FieldDescriptorProto_TYPE_INT64, descriptorpb.FieldDescriptorProto_LABEL_REPEATED, nil)
}

func repeatedUint64(num int32, name string) *descriptorpb.FieldDescriptorProto {
	return fieldDesc(num, name, descriptorpb.FieldDescriptorProto_TYPE_UINT64, descriptorpb.FieldDescriptorProto_LABEL_REPEATED, nil)
}

func repeatedStr(num int32, name string) *descriptorpb.FieldDescriptorProto {
	return fieldDesc(num, name, descriptorpb.FieldDescriptorProto_TYPE_STRING, descriptorpb.FieldDescriptorProto_LABEL_REPEATED, nil)
}

func optionalMsg(num int32, name, typeName string) *descriptorpb.FieldDescriptorProto {
	return fieldDesc(num, name, descriptorpb.FieldDescriptorProto_TYPE_MESSAGE, descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL, strPtr(typeName))
}

func repeatedMsg(num int32, name, typeName string) *descriptorpb.FieldDescriptorProto {
	return fieldDesc(num, name, descriptorpb.FieldDescriptorProto_TYPE_MESSAGE, descriptorpb.FieldDescriptorProto_LABEL_REPEATED, strPtr(typeName))
}
