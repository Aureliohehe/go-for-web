package main

import "testing"

func TestLoadDBConfigMissingPassword(t *testing.T) {
	t.Setenv("MYSQL_PASSWORD", "")

	_, err := loadDBConfig()
	if err == nil {
		t.Fatal("密码为空时应返回错误，但得到 nil")
	}
}

func TestLoadDBConfigSuccess(t *testing.T) {
	t.Setenv("MYSQL_HOST", "127.0.0.1")
	t.Setenv("MYSQL_PORT", "13306")
	t.Setenv("MYSQL_USER", "test_user")
	t.Setenv("MYSQL_PASSWORD", "test_password")
	t.Setenv("MYSQL_DATABASE", "test_db")

	cfg, err := loadDBConfig()
	if err != nil {
		t.Fatalf("配置完整时不应报错：%v", err)
	}

	want := DBConfig{
		Host:     "127.0.0.1",
		Port:     "13306",
		User:     "test_user",
		Password: "test_password",
		Database: "test_db",
	}
	if cfg != want {
		t.Fatal("读取到的配置与预期不一致")
	}
}

func TestLoadDBConfigDefaults(t *testing.T) {

	t.Setenv("MYSQL_HOST", "")
	t.Setenv("MYSQL_PORT", "")
	t.Setenv("MYSQL_USER", "")
	t.Setenv("MYSQL_PASSWORD", "test_password")
	t.Setenv("MYSQL_DATABASE", "")
	cfg, err := loadDBConfig()
	if err != nil {
		t.Fatalf("配置完整时不应报错：%v", err)
	}
	want := DBConfig{
		Host:     "127.0.0.1",
		Port:     "13306",
		User:     "hookrelay",
		Password: "test_password",
		Database: "hookrelay",
	}
	if cfg != want {
		t.Fatal("读取到的配置与预期不一致")
	}
}

func TestLoadDBConfigPort(t *testing.T) {
	tests := []struct {
		name    string
		port    string
		wantErr bool
	}{
		{name: "minimum", port: "1"},
		{name: "maximum", port: "65535"},
		{name: "normal", port: "13306"},
		{name: "empty uses default", port: ""},
		{name: "not a number", port: "abc", wantErr: true},
		{name: "zero", port: "0", wantErr: true},
		{name: "negative", port: "-1", wantErr: true},
		{name: "too large", port: "65536", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("MYSQL_PASSWORD", "test_password")
			t.Setenv("MYSQL_PORT", tt.port)

			_, err := loadDBConfig()
			if (err != nil) != tt.wantErr {
				t.Fatalf("error = %v, wantErr = %v", err, tt.wantErr)
			}
		})
	}
}
