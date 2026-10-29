package main

import "testing"

func TestOnlyThrowawayLoopbackDatabasesAreAccepted(t *testing.T) {
	for _, bad := range []string{
		"postgres://studio:studio@127.0.0.1:5433/studio",
		"postgres://studio:studio@127.0.0.1:5433/postgres",
		"postgres://studio:studio@127.0.0.1:5433/studio_test_1",
		"postgres://studio:studio@127.0.0.1:5433/verify_",
		"postgres://studio:studio@127.0.0.1:5433/xverify_1",
		"postgres://studio:studio@127.0.0.1:5433/verify_1;drop",
		"postgres://studio:studio@db.example.com:5432/verify_1",
		"postgres://studio:studio@10.0.0.5:5432/verify_1",
		"",
	} {
		if err := validate(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	for _, good := range []string{
		"postgres://studio:studio@127.0.0.1:5433/verify_1730000000_1234",
		"postgres://studio:studio@localhost:5433/studio_restore_1730000000_ab12",
		"postgres://studio:studio@127.0.0.1:5433/studio_rt_src_1_ab",
	} {
		if err := validate(good); err != nil {
			t.Errorf("%q refused: %v", good, err)
		}
	}
}
