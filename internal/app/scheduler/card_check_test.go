package scheduler

import "testing"

func TestCanSchedule(t *testing.T) {
	tests := []struct {
		name string
		card Card
		want bool
	}{
		{
			name: "todo с закрытыми зависимостями",
			card: Card{
				Status:     "todo",
				OwnerFiles: []string{"internal/app/scheduler/card_check.go"},
				Dependencies: []Dependency{
					{Ref: "BS.24", Closed: true},
					{Ref: "BS.23", Closed: true},
				},
			},
			want: true,
		},
		{
			name: "done не планируется",
			card: Card{
				Status:     "done",
				OwnerFiles: []string{"internal/app/scheduler/card_check.go"},
			},
			want: false,
		},
		{
			name: "нет OwnerFiles",
			card: Card{
				Status:       "todo",
				Dependencies: []Dependency{{Ref: "BS.24", Closed: true}},
			},
			want: false,
		},
		{
			name: "открытая зависимость",
			card: Card{
				Status:     "todo",
				OwnerFiles: []string{"internal/app/scheduler/card_check.go"},
				Dependencies: []Dependency{
					{Ref: "BS.24", Closed: true},
					{Ref: "BS.25", Closed: false},
				},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ok, reason := CanSchedule(tt.card)
			if ok != tt.want {
				t.Fatalf("CanSchedule() = %v, хотим %v; причина: %q", ok, tt.want, reason)
			}
			if ok && reason != "" {
				t.Fatalf("при успехе причина должна быть пустой, получено %q", reason)
			}
			if !ok && reason == "" {
				t.Fatal("при отказе должна быть непустая причина")
			}
		})
	}
}
