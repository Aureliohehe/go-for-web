package config

import "testing"

func TestLoad(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		wantErr bool
	}{
		{name: "defaults", env: map[string]string{"MYSQL_PASSWORD": "test"}},
		{name: "missing password", wantErr: true},
		{name: "invalid mysql port", env: map[string]string{"MYSQL_PASSWORD": "test", "MYSQL_PORT": "oops"}, wantErr: true},
		{name: "zero mysql port", env: map[string]string{"MYSQL_PASSWORD": "test", "MYSQL_PORT": "0"}, wantErr: true},
		{name: "invalid http address", env: map[string]string{"MYSQL_PASSWORD": "test", "HTTP_ADDR": "8080"}, wantErr: true},
		{name: "invalid http port", env: map[string]string{"MYSQL_PASSWORD": "test", "HTTP_ADDR": "127.0.0.1:65536"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := load(func(key string) string { return tt.env[key] })
			if (err != nil) != tt.wantErr {
				t.Fatalf("Load error = %v, wantErr = %v", err, tt.wantErr)
			}
			if !tt.wantErr && (cfg.MySQLPort != "13306" || cfg.HTTPAddr != "127.0.0.1:8080") {
				t.Fatal("unexpected defaults")
			}
		})
	}
}

func TestLoadOverrides(t *testing.T) {
	env := map[string]string{
		"HTTP_ADDR": "127.0.0.1:9090", "MYSQL_HOST": "localhost",
		"MYSQL_PORT": "23306", "MYSQL_DATABASE": "testdb",
		"MYSQL_USER": "testuser", "MYSQL_PASSWORD": "testpass",
	}
	cfg, err := load(func(key string) string { return env[key] })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTPAddr != env["HTTP_ADDR"] || cfg.MySQLHost != env["MYSQL_HOST"] ||
		cfg.MySQLPort != env["MYSQL_PORT"] || cfg.MySQLDatabase != env["MYSQL_DATABASE"] ||
		cfg.MySQLUser != env["MYSQL_USER"] || cfg.MySQLPassword != env["MYSQL_PASSWORD"] {
		t.Fatal("environment overrides were not applied")
	}
}
