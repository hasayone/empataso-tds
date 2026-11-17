package ktimport

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// LoadEnv читает KEY=VALUE из файла и не затирает уже заданные переменные.
//
// Параметры: path — путь к .env.
// Результат: переменные попали в окружение процесса; nil.
// Ошибки: файл не открылся или строка без знака «=».
func LoadEnv(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("env: %w", err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			return fmt.Errorf("env: строка %d без знака равенства", lineNo)
		}
		k = strings.TrimSpace(k)
		v = strings.Trim(strings.TrimSpace(v), `"'`)
		if k == "" {
			return fmt.Errorf("env: пустое имя в строке %d", lineNo)
		}
		if _, exists := os.LookupEnv(k); exists {
			continue
		}
		if err := os.Setenv(k, v); err != nil {
			return fmt.Errorf("env: %w", err)
		}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("env: %w", err)
	}
	return nil
}

// Env читает переменную окружения.
//
// Параметры: key — полное имя, например EMTDS_ADMIN_URL.
// Результат: значение или пустая строка, если переменная не задана.
// Ошибки: нет.
func Env(key string) string {
	return os.Getenv(key)
}

// RequireLiveEnv проверяет адрес и ключ до любого сетевого вызова.
//
// Параметры: нет. Читает KEITARO_BASE_URL и KEITARO_API_KEY.
// Результат: адрес без завершающего слэша и ключ.
// Ошибки: пустой адрес или пустой ключ.
func RequireLiveEnv() (base, key string, err error) {
	base = strings.TrimRight(strings.TrimSpace(os.Getenv("KEITARO_BASE_URL")), "/")
	key = strings.TrimSpace(os.Getenv("KEITARO_API_KEY"))
	if base == "" || key == "" {
		return "", "", fmt.Errorf("env: для --live нужны KEITARO_BASE_URL и KEITARO_API_KEY")
	}
	return base, key, nil
}
