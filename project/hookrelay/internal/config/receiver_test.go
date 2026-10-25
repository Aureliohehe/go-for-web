package config

import "testing"

func TestReceiverDBConfig(t *testing.T) {
	values := map[string]string{"RECEIVER_MYSQL_PASSWORD": "test-only", "MYSQL_DATABASE": "platform", "MYSQL_PASSWORD": "not-used"}
	get := func(key string) string { return values[key] }
	cfg, err := loadReceiverDB(get)
	if err != nil || cfg.MySQLDatabase != "hookrelay_receiver" || cfg.MySQLUser != "hookrelay_receiver" || cfg.MySQLPassword != "test-only" || cfg.MySQLPort != "13306" {
		t.Fatal("receiver must use separate configuration")
	}
	values["RECEIVER_MYSQL_DATABASE"] = "recipient_test"
	values["RECEIVER_MYSQL_PORT"] = "23306"
	cfg, err = loadReceiverDB(get)
	if err != nil || cfg.MySQLDatabase != "recipient_test" || cfg.MySQLPort != "23306" {
		t.Fatal("receiver overrides ignored")
	}
	values["RECEIVER_MYSQL_PORT"] = "0"
	if _, err := loadReceiverDB(get); err == nil {
		t.Fatal("invalid port accepted")
	}
	delete(values, "RECEIVER_MYSQL_PASSWORD")
	if _, err := loadReceiverDB(get); err == nil {
		t.Fatal("platform password must not be inherited")
	}
}
