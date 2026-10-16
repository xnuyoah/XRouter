package common

import (
	"errors"
	"testing"
)

func TestUserTurnDropsRefusesAnEmptiedTurnEvenWhenOtherTurnsRemain(t *testing.T) {
	var drops UserTurnDrops
	drops.EndTurn(1) // an earlier normal turn
	drops.Drop("container_upload")
	drops.EndTurn(0) // the turn the attachment emptied
	drops.EndTurn(3) // a later normal turn must not clear the refusal

	var unsupported *UnsupportedPartError
	err := drops.Err()
	if !errors.As(err, &unsupported) || unsupported.Type != "container_upload" {
		t.Fatalf("err = %v, want unsupported content part: container_upload", err)
	}
	if unsupported.StatusCode() != 400 || !unsupported.IsRequestScoped() {
		t.Fatalf("err = %#v", unsupported)
	}
}

func TestUserTurnDropsKeepsATurnThatStillHasSendableParts(t *testing.T) {
	var drops UserTurnDrops
	drops.Drop("file")
	drops.EndTurn(1)
	if err := drops.Err(); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
}

func TestUserTurnDropsIgnoresAnEmptyTurnWithoutADroppedPart(t *testing.T) {
	var drops UserTurnDrops
	drops.EndTurn(0)
	if err := drops.Err(); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
}

func TestUserTurnDropsReportsTheFirstEmptiedTurn(t *testing.T) {
	var drops UserTurnDrops
	drops.Drop("document")
	drops.Drop("container_upload")
	drops.EndTurn(0)
	drops.Drop("file")
	drops.EndTurn(0)
	if got, want := drops.Err().Error(), "unsupported content part: document"; got != want {
		t.Fatalf("error text = %q, want %q", got, want)
	}
}

func TestUserTurnDropsDoesNotLeakAFlagIntoTheNextTurn(t *testing.T) {
	var drops UserTurnDrops
	drops.Drop("file")
	drops.EndTurn(1)
	drops.EndTurn(0)
	if err := drops.Err(); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
}

func TestUserRunRefusesATurnEmptiedByADroppedPart(t *testing.T) {
	var run UserRun
	run.Add()
	run.End() // an earlier normal turn
	run.Drop("image")
	run.End() // the turn the attachment emptied
	run.Add()
	run.End() // a later normal turn must not clear the refusal
	if got, want := run.Err().Error(), "unsupported content part: image"; got != want {
		t.Fatalf("error text = %q, want %q", got, want)
	}
}

func TestUserRunKeepsATurnWithOtherSendableContent(t *testing.T) {
	var run UserRun
	run.Drop("audio")
	run.Add()
	run.End()
	if err := run.Err(); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
}

func TestUserRunIsANoOpOnANilReceiver(t *testing.T) {
	var run *UserRun
	run.Add()
	run.Drop("image")
	run.End()
	if err := run.Err(); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
}

func TestIsHTTPURL(t *testing.T) {
	cases := map[string]bool{
		"https://example.test/a.png": true,
		"HTTP://example.test/a.png":  true,
		"  https://example.test/a  ": true,
		"gs://bucket/a.pdf":          false,
		"files/abc123":               false,
		"ftp://example.test/a.png":   false,
		"data:image/png;base64,aGk=": false,
		"https://":                   false,
		"":                           false,
	}
	for value, want := range cases {
		if got := IsHTTPURL(value); got != want {
			t.Fatalf("IsHTTPURL(%q) = %v, want %v", value, got, want)
		}
	}
}
