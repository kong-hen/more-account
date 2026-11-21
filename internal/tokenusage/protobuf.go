package tokenusage

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

const maxPositiveInt64 = uint64(0x7fff_ffff_ffff_ffff)

// ParsedUsageProto 表示从 steps.metadata.f9 或 gen_metadata.data.f1.f4 解码的精确 Token 遥测。
type ParsedUsageProto struct {
	ModelEnum            uint64
	APIProvider          uint64
	InputTokens          uint64
	OutputTokens         uint64
	ThinkingTokens       uint64
	CacheReadTokens      uint64
	CacheWriteTokens     uint64
	TotalTokens          uint64
	MessageID            string
}

// ParsedStepMetadata 表示 steps.metadata 字段解码结果。
type ParsedStepMetadata struct {
	TimestampMs       int64
	Usage             *ParsedUsageProto
	FallbackModelEnum uint64
}

// ParsedGenMetadata 表示 gen_metadata.data 字段解码结果。
type ParsedGenMetadata struct {
	StepIndices          []int
	ModelEnumNumeric     uint64
	ModelEnumPlaceholder string
	ResponseModel        string
	Usage                *ParsedUsageProto
}

type protoWireVal struct {
	wireType uint8
	varint   uint64
	bytes    []byte
}

func readVarint(buf []byte, offset int) (uint64, int, bool) {
	if offset >= len(buf) {
		return 0, offset, false
	}
	var res uint64
	for i := 0; i < 10; i++ {
		idx := offset + i
		if idx >= len(buf) {
			return 0, offset, false
		}
		b := buf[idx]
		if i == 9 {
			if b > 0x01 {
				return 0, offset, false
			}
			res |= uint64(b) << 63
			return res, idx + 1, true
		}
		res |= uint64(b&0x7f) << (uint(i) * 7)
		if b&0x80 == 0 {
			return res, idx + 1, true
		}
	}
	return 0, offset, false
}

func visitProtoFields(buf []byte, fn func(fieldNum uint32, val protoWireVal)) {
	offset := 0
	for offset < len(buf) {
		tag, afterTag, ok := readVarint(buf, offset)
		if !ok {
			return
		}
		fieldNum64 := tag >> 3
		wireType := uint8(tag & 0x07)
		if fieldNum64 == 0 || fieldNum64 > 0xffff_ffff {
			return
		}
		fieldNum := uint32(fieldNum64)

		switch wireType {
		case 0:
			v, next, ok := readVarint(buf, afterTag)
			if !ok {
				return
			}
			fn(fieldNum, protoWireVal{wireType: 0, varint: v})
			offset = next
		case 1:
			if afterTag+8 > len(buf) {
				return
			}
			fn(fieldNum, protoWireVal{wireType: 1, bytes: buf[afterTag : afterTag+8]})
			offset = afterTag + 8
		case 2:
			rawLen, afterLen, ok := readVarint(buf, afterTag)
			if !ok || rawLen > uint64(len(buf)-afterLen) {
				return
			}
			end := afterLen + int(rawLen)
			fn(fieldNum, protoWireVal{wireType: 2, bytes: buf[afterLen:end]})
			offset = end
		case 5:
			if afterTag+4 > len(buf) {
				return
			}
			fn(fieldNum, protoWireVal{wireType: 5, bytes: buf[afterTag : afterTag+4]})
			offset = afterTag + 4
		default:
			return
		}
	}
}

func varintToTokenCount(v uint64) uint64 {
	if v > maxPositiveInt64 {
		return 0
	}
	return v
}

func parseProtoTimestampMs(buf []byte) int64 {
	var secs uint64
	var nanos uint64
	visitProtoFields(buf, func(fieldNum uint32, val protoWireVal) {
		if val.wireType != 0 {
			return
		}
		if fieldNum == 1 && val.varint > 0 && val.varint <= maxPositiveInt64 {
			secs = val.varint
		} else if fieldNum == 2 && val.varint <= maxPositiveInt64 {
			nanos = val.varint
		}
	})
	if secs == 0 {
		return 0
	}
	return int64(secs)*1000 + int64(nanos/1_000_000)
}

func formatUTCMs(ms int64) string {
	if ms <= 0 {
		return ""
	}
	return time.UnixMilli(ms).UTC().Format("2006-01-02T15:04:05.000Z")
}

// ParseUsageProto 解码单条 API Token 遥测，并严格保证 Thinking 不重复计入 OutputTokens。
func ParseUsageProto(buf []byte) (ParsedUsageProto, bool) {
	if len(buf) == 0 {
		return ParsedUsageProto{}, false
	}
	var (
		modelEnum       uint64
		apiProvider     uint64
		inputTokens     uint64
		rawCombinedOut  uint64
		hasCombinedOut  bool
		cacheWrite      uint64
		cacheRead       uint64
		thinking        uint64
		respOut         uint64
		hasRespOut      bool
		messageID       string
		sawRecognized   bool
	)

	visitProtoFields(buf, func(fieldNum uint32, val protoWireVal) {
		switch fieldNum {
		case 1:
			if val.wireType == 0 {
				sawRecognized = true
				if val.varint > 0 && val.varint <= maxPositiveInt64 {
					modelEnum = val.varint
				}
			}
		case 2:
			if val.wireType == 0 {
				sawRecognized = true
				inputTokens = varintToTokenCount(val.varint)
			}
		case 3:
			if val.wireType == 0 {
				sawRecognized = true
				rawCombinedOut = varintToTokenCount(val.varint)
				hasCombinedOut = true
			}
		case 4:
			if val.wireType == 0 {
				sawRecognized = true
				cacheWrite = varintToTokenCount(val.varint)
			}
		case 5:
			if val.wireType == 0 {
				sawRecognized = true
				cacheRead = varintToTokenCount(val.varint)
			}
		case 6:
			if val.wireType == 0 {
				sawRecognized = true
				if val.varint > 0 && val.varint <= maxPositiveInt64 {
					apiProvider = val.varint
				}
			}
		case 7:
			if val.wireType == 2 {
				sawRecognized = true
				if utf8.Valid(val.bytes) {
					messageID = strings.TrimSpace(string(val.bytes))
				}
			}
		case 9:
			if val.wireType == 0 {
				sawRecognized = true
				thinking = varintToTokenCount(val.varint)
			}
		case 10:
			if val.wireType == 0 {
				sawRecognized = true
				respOut = varintToTokenCount(val.varint)
				hasRespOut = true
			}
		}
	})

	if !sawRecognized {
		return ParsedUsageProto{}, false
	}

	var outputTokens uint64
	if hasRespOut {
		outputTokens = respOut
	} else if hasCombinedOut && rawCombinedOut > thinking {
		outputTokens = rawCombinedOut - thinking
	}

	total := inputTokens + outputTokens + thinking + cacheRead + cacheWrite
	return ParsedUsageProto{
		ModelEnum:        modelEnum,
		APIProvider:      apiProvider,
		InputTokens:      inputTokens,
		OutputTokens:     outputTokens,
		ThinkingTokens:   thinking,
		CacheReadTokens:  cacheRead,
		CacheWriteTokens: cacheWrite,
		TotalTokens:      total,
		MessageID:        messageID,
	}, true
}

// ParseStepMetadata 解码 steps.metadata 字段。
func ParseStepMetadata(buf []byte) ParsedStepMetadata {
	var out ParsedStepMetadata
	visitProtoFields(buf, func(fieldNum uint32, val protoWireVal) {
		switch fieldNum {
		case 1:
			if val.wireType == 2 {
				if ts := parseProtoTimestampMs(val.bytes); ts > 0 {
					out.TimestampMs = ts
				}
			}
		case 9:
			if val.wireType == 2 {
				if u, ok := ParseUsageProto(val.bytes); ok {
					out.Usage = &u
				}
			}
		case 11:
			if val.wireType == 0 && val.varint > 0 && val.varint <= maxPositiveInt64 {
				out.FallbackModelEnum = val.varint
			}
		}
	})
	return out
}

// ParseGenMetadata 解码 gen_metadata.data 字段。
func ParseGenMetadata(buf []byte) ParsedGenMetadata {
	var out ParsedGenMetadata
	visitProtoFields(buf, func(fieldNum uint32, val protoWireVal) {
		switch fieldNum {
		case 2:
			if val.wireType == 2 {
				pos := 0
				for pos < len(val.bytes) {
					v, next, ok := readVarint(val.bytes, pos)
					if !ok {
						break
					}
					if v <= maxPositiveInt64 {
						out.StepIndices = append(out.StepIndices, int(v))
					}
					pos = next
				}
			} else if val.wireType == 0 && val.varint <= maxPositiveInt64 {
				out.StepIndices = append(out.StepIndices, int(val.varint))
			}
		case 1:
			if val.wireType != 2 {
				return
			}
			visitProtoFields(val.bytes, func(innerNum uint32, innerVal protoWireVal) {
				switch innerNum {
				case 3:
					if innerVal.wireType == 0 && innerVal.varint > 0 && innerVal.varint <= maxPositiveInt64 {
						out.ModelEnumNumeric = innerVal.varint
					}
				case 4:
					if innerVal.wireType == 2 && len(innerVal.bytes) > 0 {
						if u, ok := ParseUsageProto(innerVal.bytes); ok {
							out.Usage = &u
						}
					}
				case 19:
					if innerVal.wireType == 2 && utf8.Valid(innerVal.bytes) {
						if s := strings.TrimSpace(string(innerVal.bytes)); s != "" {
							out.ResponseModel = s
						}
					}
				case 20:
					if innerVal.wireType == 2 {
						var k, v string
						visitProtoFields(innerVal.bytes, func(kvNum uint32, kvVal protoWireVal) {
							if kvVal.wireType == 2 && utf8.Valid(kvVal.bytes) {
								if kvNum == 1 {
									k = string(kvVal.bytes)
								} else if kvNum == 2 {
									v = strings.TrimSpace(string(kvVal.bytes))
								}
							}
						})
						if k == "model_enum" && v != "" {
							out.ModelEnumPlaceholder = v
						}
					}
				}
			})
		}
	})
	return out
}

// estimateTokensFromPayloadLen 当步骤缺少精确 API 遥测时，按载荷字节长度做启发式估算（无需将大 BLOB 读入 Go 内存）。
func estimateTokensFromPayloadLen(payloadLen int64) uint64 {
	if payloadLen <= 0 {
		return 0
	}
	est := uint64(payloadLen) / 4
	if est == 0 {
		return 1
	}
	return est
}

func formatEnumKey(num uint64) string {
	if num == 0 {
		return ""
	}
	return fmt.Sprintf("%d", num)
}
