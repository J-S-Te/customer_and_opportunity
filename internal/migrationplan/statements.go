package migrationplan

import (
	"errors"
	"strings"
)

// SplitStatements splits the repository-maintained MySQL migration files into
// statement-level checkpoints. The migration baseline does not permit stored
// program DELIMITER blocks; quoted strings, identifiers and comments are still
// handled so semicolons inside them never become execution boundaries.
func SplitStatements(contents string) ([]string, error) {
	const (
		plain = iota
		singleQuoted
		doubleQuoted
		backtickQuoted
		lineComment
		blockComment
	)
	state := plain
	escaped := false
	hasToken := false
	start := 0
	statements := make([]string, 0, strings.Count(contents, ";"))

	appendStatement := func(end int) {
		if hasToken {
			statements = append(statements, strings.TrimSpace(contents[start:end]))
		}
		hasToken = false
	}

	for index := 0; index < len(contents); index++ {
		current := contents[index]
		next := byte(0)
		if index+1 < len(contents) {
			next = contents[index+1]
		}
		switch state {
		case lineComment:
			if current == '\n' {
				state = plain
			}
			continue
		case blockComment:
			if current == '*' && next == '/' {
				index++
				state = plain
			}
			continue
		case singleQuoted, doubleQuoted, backtickQuoted:
			quote := byte('\'')
			if state == doubleQuoted {
				quote = '"'
			} else if state == backtickQuoted {
				quote = '`'
			}
			if escaped {
				escaped = false
				continue
			}
			if current == '\\' && state != backtickQuoted {
				escaped = true
				continue
			}
			if current == quote {
				if next == quote {
					index++
					continue
				}
				state = plain
			}
			continue
		}

		if current == '#' {
			state = lineComment
			continue
		}
		if current == '-' && next == '-' {
			state = lineComment
			index++
			continue
		}
		if current == '/' && next == '*' {
			state = blockComment
			index++
			continue
		}
		switch current {
		case '\'':
			state = singleQuoted
			hasToken = true
		case '"':
			state = doubleQuoted
			hasToken = true
		case '`':
			state = backtickQuoted
			hasToken = true
		case ';':
			appendStatement(index)
			start = index + 1
		default:
			if current != ' ' && current != '\t' && current != '\r' && current != '\n' {
				hasToken = true
			}
		}
	}
	if state == blockComment {
		return nil, errors.New("unterminated block comment")
	}
	if state == singleQuoted || state == doubleQuoted || state == backtickQuoted || escaped {
		return nil, errors.New("unterminated quoted value")
	}
	appendStatement(len(contents))
	if len(statements) == 0 {
		return nil, errors.New("migration contains no executable statements")
	}
	return statements, nil
}
