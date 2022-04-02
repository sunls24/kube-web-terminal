package main

func CheckEmpty(list ...string) bool {
	for i := range list {
		if len(list[i]) == 0 {
			return true
		}
	}
	return false
}
