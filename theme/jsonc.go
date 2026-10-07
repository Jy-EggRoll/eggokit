package theme

import "bytes"

// sanitizeJSONC 把 JSONC 规整成标准 JSON：去掉 BOM、行注释、块注释与尾逗号。
//
// 为什么必须做：VSCode 的主题文件本来就是 JSONC——官方那几套里就有 `//` 注释与末尾逗号，
// 而 encoding/json 只认标准 JSON。不做这一步，官方的主题文件一个都读不了。
//
// 为什么不能只做正则替换：主题文件的 name、description 里会出现 "//"（例如一句网址），
// 正则会把字符串里的 // 当成注释起点、把后面的内容一起当成注释，表现为“解析成功但颜色少了一半”。
// 这里用一个只认三种状态（字符串内 / 转义中 / 普通文本）的扫描，字符串里的内容一律照抄。
//
// 尾逗号在同一次扫描里判定：记下刚输出的那个逗号的位置，等下一个非空白字符出现时，
// 如果它是 `}` 或 `]`，就把那个逗号从结果里抠掉。注释与空白不影响这个判定，
// 因此也不需要在去注释之后再来一遍
func sanitizeJSONC(src []byte) []byte {
	// UTF-8 BOM：有些编辑器另存时会带上，而 encoding/json 不认它
	src = bytes.TrimPrefix(src, []byte{0xEF, 0xBB, 0xBF})

	out := make([]byte, 0, len(src))
	inString, escaped := false, false
	commaAt := -1 // 待判定的逗号在 out 中的位置；-1 表示当前没有

	for i := 0; i < len(src); i++ {
		ch := src[i]

		if inString {
			out = append(out, ch)
			switch {
			case escaped:
				escaped = false
			case ch == '\\':
				escaped = true
			case ch == '"':
				inString = false
				commaAt = -1 // 字符串是逗号后面的有效内容，这个逗号不是尾逗号
			}
			continue
		}

		switch ch {
		case '"':
			inString = true
			commaAt = -1
			out = append(out, ch)
		case '/':
			switch {
			case i+1 < len(src) && src[i+1] == '/':
				for i < len(src) && src[i] != '\n' {
					i++
				}
				// 把换行留下：两行粘成一行时若行尾注释去掉了分隔符，JSON 结构会变
				if i < len(src) {
					out = append(out, '\n')
				}
			case i+1 < len(src) && src[i+1] == '*':
				i += 2
				for i+1 < len(src) && !(src[i] == '*' && src[i+1] == '/') {
					i++
				}
				i++ // 停在 '/' 上，交给循环再 ++ 就越过它
			default:
				commaAt = -1
				out = append(out, ch)
			}
		case ' ', '\t', '\n', '\r':
			out = append(out, ch) // 空白不改变逗号的判定
		case ',':
			commaAt = len(out)
			out = append(out, ch)
		case '}', ']':
			if commaAt >= 0 {
				out = append(out[:commaAt], out[commaAt+1:]...)
			}
			commaAt = -1
			out = append(out, ch)
		default:
			commaAt = -1
			out = append(out, ch)
		}
	}
	return out
}
