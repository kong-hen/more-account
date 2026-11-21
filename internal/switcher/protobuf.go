package switcher

import (
	"encoding/base64"
	"fmt"
	"strings"
)

// EncodeVarint 将无符号整数编码为 Protobuf Varint 字节序列。
func EncodeVarint(v uint64) []byte {
	var buf []byte
	for v >= 0x80 {
		buf = append(buf, byte(v&0x7F)|0x80)
		v >>= 7
	}
	buf = append(buf, byte(v))
	return buf
}

// EncodeLenDelimField 编码长度分隔字段 (wire_type = 2)。
func EncodeLenDelimField(fieldNum uint32, data []byte) []byte {
	tag := (uint64(fieldNum) << 3) | 2
	out := EncodeVarint(tag)
	out = append(out, EncodeVarint(uint64(len(data)))...)
	out = append(out, data...)
	return out
}

// EncodeStringField 编码 UTF-8 字符串字段 (wire_type = 2)。
func EncodeStringField(fieldNum uint32, val string) []byte {
	return EncodeLenDelimField(fieldNum, []byte(val))
}

// EncodeVarintField 编码 Varint 字段 (wire_type = 0)。
func EncodeVarintField(fieldNum uint32, val uint64) []byte {
	tag := (uint64(fieldNum) << 3) | 0
	out := EncodeVarint(tag)
	out = append(out, EncodeVarint(val)...)
	return out
}

// ReadVarint 从字节流指定偏移读取 Varint。
func ReadVarint(data []byte, offset int) (uint64, int, error) {
	var result uint64
	var shift uint
	pos := offset
	for {
		if pos >= len(data) {
			return 0, pos, fmt.Errorf("protobuf 数据不完整")
		}
		b := data[pos]
		if shift == 63 && b > 0x01 {
			return 0, pos, fmt.Errorf("protobuf varint 溢出")
		}
		result |= uint64(b&0x7F) << shift
		pos++
		if b&0x80 == 0 {
			break
		}
		shift += 7
		if shift > 63 {
			return 0, pos, fmt.Errorf("protobuf varint 溢出")
		}
	}
	return result, pos, nil
}

// sliceLenDelim 安全读取长度分隔字段切片，使用无符号剩余长度比较彻底防止大 varint 转换溢出导致切片越界 panic。
func sliceLenDelim(data []byte, offset int) ([]byte, int, error) {
	length, contentOffset, err := ReadVarint(data, offset)
	if err != nil {
		return nil, offset, err
	}
	if contentOffset < 0 || contentOffset > len(data) || length > uint64(len(data)-contentOffset) {
		return nil, offset, fmt.Errorf("长度分隔字段越界")
	}
	end := contentOffset + int(length)
	return data[contentOffset:end], end, nil
}

// SkipField 按 wire_type 跳过当前字段。
func SkipField(data []byte, offset int, wireType uint8) (int, error) {
	switch wireType {
	case 0:
		_, next, err := ReadVarint(data, offset)
		return next, err
	case 1:
		if offset+8 > len(data) {
			return offset, fmt.Errorf("fixed64 数据不足")
		}
		return offset + 8, nil
	case 2:
		_, end, err := sliceLenDelim(data, offset)
		return end, err
	case 5:
		if offset+4 > len(data) {
			return offset, fmt.Errorf("fixed32 数据不足")
		}
		return offset + 4, nil
	default:
		return offset, fmt.Errorf("未知 protobuf wire_type: %d", wireType)
	}
}

// CreateOAuthTokenInfo 编码 Antigravity 客户端兼容的 OAuthTokenInfo 消息。
func CreateOAuthTokenInfo(accessToken, refreshToken string, expiry int64, isGcpTos *bool, idToken, email string) []byte {
	gcpTos := false
	if isGcpTos != nil {
		gcpTos = *isGcpTos
	}
	lowerEmail := strings.ToLower(strings.TrimSpace(email))
	if strings.HasSuffix(lowerEmail, "@gmail.com") || strings.HasSuffix(lowerEmail, "@googlemail.com") {
		gcpTos = false
	}

	var tsMsg []byte
	tsMsg = append(tsMsg, EncodeVarintField(1, uint64(expiry))...)
	tsMsg = append(tsMsg, EncodeVarintField(2, 0)...)

	var out []byte
	out = append(out, EncodeStringField(1, accessToken)...)
	out = append(out, EncodeStringField(2, "Bearer")...)
	out = append(out, EncodeStringField(3, refreshToken)...)
	out = append(out, EncodeLenDelimField(4, tsMsg)...)
	if trimmedID := strings.TrimSpace(idToken); trimmedID != "" {
		out = append(out, EncodeStringField(5, trimmedID)...)
	}
	if gcpTos {
		out = append(out, EncodeVarintField(6, 1)...)
	}
	return out
}

// CreateUnifiedTopicEntry 构造 unified-state Topic.data 的 map entry。
func CreateUnifiedTopicEntry(sentinelKey string, payload []byte) []byte {
	row := EncodeStringField(1, base64.StdEncoding.EncodeToString(payload))
	var entry []byte
	entry = append(entry, EncodeStringField(1, sentinelKey)...)
	entry = append(entry, EncodeLenDelimField(2, row)...)
	return EncodeLenDelimField(1, entry)
}

// CreateStringValuePayload 构造 unified-state 字符串值载荷。
func CreateStringValuePayload(val string) []byte {
	return EncodeStringField(3, val)
}

// CreateMinimalUserStatusPayload 构造最小可用 UserStatus 载荷。
func CreateMinimalUserStatusPayload(email string) []byte {
	var out []byte
	out = append(out, EncodeStringField(3, email)...)
	out = append(out, EncodeStringField(7, email)...)
	return out
}

// RemoveUnifiedTopicEntry 移除 Topic.data 中指定 sentinel key 对应的条目，保留其他条目。
func RemoveUnifiedTopicEntry(data []byte, targetKey string) ([]byte, error) {
	var result []byte
	offset := 0
	for offset < len(data) {
		start := offset
		tag, afterTag, err := ReadVarint(data, offset)
		if err != nil {
			return nil, err
		}
		wireType := uint8(tag & 7)
		fieldNum := uint32(tag >> 3)
		nextOffset, err := SkipField(data, afterTag, wireType)
		if err != nil {
			return nil, err
		}

		shouldRemove := false
		if fieldNum == 1 && wireType == 2 {
			entryBytes, _, err := sliceLenDelim(data, afterTag)
			if err != nil {
				return nil, fmt.Errorf("Topic.data entry 数据不完整: %w", err)
			}
			if unifiedTopicEntryKey(entryBytes) == targetKey {
				shouldRemove = true
			}
		}

		if !shouldRemove {
			result = append(result, data[start:nextOffset]...)
		}
		offset = nextOffset
	}
	return result, nil
}

func unifiedTopicEntryKey(entry []byte) string {
	offset := 0
	for offset < len(entry) {
		tag, afterTag, err := ReadVarint(entry, offset)
		if err != nil {
			return ""
		}
		wireType := uint8(tag & 7)
		fieldNum := uint32(tag >> 3)
		if fieldNum == 1 && wireType == 2 {
			val, _, err := sliceLenDelim(entry, afterTag)
			if err != nil {
				return ""
			}
			return string(val)
		}
		next, err := SkipField(entry, afterTag, wireType)
		if err != nil {
			return ""
		}
		offset = next
	}
	return ""
}

// ExtractOAuthInfoFromUnifiedOAuth 从 antigravityUnifiedStateSync.oauthToken 中提取 access_token、refresh_token 与 id_token。
func ExtractOAuthInfoFromUnifiedOAuth(data []byte) (accessToken, refreshToken, idToken string) {
	payload := extractUnifiedTopicPayload(data, "oauthTokenInfoSentinelKey")
	if len(payload) == 0 {
		return "", "", ""
	}
	return extractStringField(payload, 1), extractStringField(payload, 3), extractStringField(payload, 5)
}

// ExtractEmailFromUnifiedUserStatus 从 antigravityUnifiedStateSync.userStatus 中提取账号邮箱。
func ExtractEmailFromUnifiedUserStatus(data []byte) string {
	payload := extractUnifiedTopicPayload(data, "userStatusSentinelKey")
	if len(payload) == 0 {
		return ""
	}
	for _, fieldNum := range []uint32{3, 7} {
		if candidate := strings.TrimSpace(extractStringField(payload, fieldNum)); strings.Contains(candidate, "@") {
			return candidate
		}
	}
	return ""
}

func extractUnifiedTopicPayload(data []byte, targetKey string) []byte {
	offset := 0
	for offset < len(data) {
		tag, afterTag, err := ReadVarint(data, offset)
		if err != nil {
			return nil
		}
		wireType := uint8(tag & 7)
		fieldNum := uint32(tag >> 3)
		if fieldNum == 1 && wireType == 2 {
			entryBytes, next, err := sliceLenDelim(data, afterTag)
			if err != nil {
				return nil
			}
			if payload := extractPayloadFromEntry(entryBytes, targetKey); len(payload) > 0 {
				return payload
			}
			offset = next
			continue
		}
		next, err := SkipField(data, afterTag, wireType)
		if err != nil {
			return nil
		}
		offset = next
	}
	return nil
}

func extractPayloadFromEntry(entry []byte, targetKey string) []byte {
	offset := 0
	matched := false
	var rowData []byte

	for offset < len(entry) {
		tag, afterTag, err := ReadVarint(entry, offset)
		if err != nil {
			return nil
		}
		wireType := uint8(tag & 7)
		fieldNum := uint32(tag >> 3)
		if wireType == 2 {
			val, next, err := sliceLenDelim(entry, afterTag)
			if err != nil {
				return nil
			}
			if fieldNum == 1 && string(val) == targetKey {
				matched = true
			} else if fieldNum == 2 {
				rowData = val
			}
			offset = next
			continue
		}
		next, err := SkipField(entry, afterTag, wireType)
		if err != nil {
			return nil
		}
		offset = next
	}

	if !matched || len(rowData) == 0 {
		return nil
	}
	payloadB64 := extractStringField(rowData, 1)
	if payloadB64 == "" {
		return nil
	}
	decoded, err := base64.StdEncoding.DecodeString(payloadB64)
	if err != nil {
		return nil
	}
	return decoded
}

func extractStringField(data []byte, targetField uint32) string {
	offset := 0
	for offset < len(data) {
		tag, afterTag, err := ReadVarint(data, offset)
		if err != nil {
			return ""
		}
		wireType := uint8(tag & 7)
		fieldNum := uint32(tag >> 3)
		if fieldNum == targetField && wireType == 2 {
			val, _, err := sliceLenDelim(data, afterTag)
			if err != nil {
				return ""
			}
			return string(val)
		}
		next, err := SkipField(data, afterTag, wireType)
		if err != nil {
			return ""
		}
		offset = next
	}
	return ""
}
