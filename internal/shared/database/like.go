package database

import "strings"

// likeEscaper 一次性完成 LIKE 通配符转义。strings.Replacer 单遍匹配且不重新处理
// 已替换出来的字符，因此反斜杠必须先于 % 和 _ 定义，替换结果不会被二次转义。
var likeEscaper = strings.NewReplacer(
	`\`, `\\`,
	`%`, `\%`,
	`_`, `\_`,
)

// EscapeLike 转义 MySQL LIKE 模式中的 \、% 和 _，使调用方关键字按字面匹配。
//
// 未转义时用户输入的 % 和 _ 会变成通配符：前导 % 迫使优化器退化为全表扫描，
// 关键字里的 _ 还能匹配到非预期行，放大索引扫描范围并改变“子串搜索”语义。
// 调用方必须在 SQL 中同时声明 ESCAPE '\\'（MySQL 字符串字面量中 \\ 表示单个
// 反斜杠），否则转义后的 \_ 会被当作普通的下划线继续匹配任意字符。
func EscapeLike(value string) string {
	return likeEscaper.Replace(value)
}

// LikeContains 返回 "%<escaped>%" 子串匹配模式，转义规则见 EscapeLike。
// 仓储层统一用它构造关键字模式，避免各模块各写一遍 "%"+value+"%"。
func LikeContains(value string) string {
	return "%" + EscapeLike(value) + "%"
}
