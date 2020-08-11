package main

func containsPlaceholder(source string) bool {
	arr := []rune(source)
	previous := ' '
	matched := false

	for i := 0; i < len(arr); i++ {
		if arr[i] == '{' && previous == '$' {
			matched = true
		} else if arr[i] == '}' && matched {
			return true
		}
		previous = arr[i]
	}
	return false
}

func replace(source string, ctx map[string]string) string {
	if ctx == nil || len(ctx) == 0 {
		return source
	}
	result := ""
	arr := []rune(source)
	key := ""
	previous := ' '
	matched := false

	for i := 0; i < len(arr); i++ {
		if arr[i] == '{' && previous == '$' {
			matched = true
			previous = arr[i]
			result = result[0 : len(result)-1]
		} else if arr[i] == '}' && matched {
			result += getValue(key, ctx)
			key = ""
			matched = false
		} else if matched {
			key += string(arr[i])
		} else {
			previous = arr[i]
			result += string(arr[i])
		}
	}
	return result
}

func getValue(key string, context map[string]string) string {
	value, ok := context[key]
	if ok {
		return value
	}
	return key
}
