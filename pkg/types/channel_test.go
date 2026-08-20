package types

import "testing"

func TestChannelCanonical(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   Channel
		want Channel
	}{
		{
			name: "already canonical",
			in:   DefaultChannel,
			want: DefaultChannel,
		},
		{
			name: "trim and lowercase",
			in:   Channel(" HARPOON "),
			want: ChannelHarpoon,
		},
		{
			name: "empty stays empty",
			in:   Channel("   "),
			want: Channel(""),
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.in.Canonical(); got != tc.want {
				t.Fatalf("Canonical() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestNormalizeChannel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		in      string
		want    Channel
		wantErr bool
	}{
		{
			name: "empty defaults to main",
			in:   "",
			want: DefaultChannel,
		},
		{
			name: "whitespace defaults to main",
			in:   "   ",
			want: DefaultChannel,
		},
		{
			name: "canonicalizes uppercase input",
			in:   " HARPOON ",
			want: ChannelHarpoon,
		},
		{
			name:    "rejects invalid spaces",
			in:      "bad channel",
			wantErr: true,
		},
		{
			name:    "rejects invalid symbols",
			in:      "main!",
			wantErr: true,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := NormalizeChannel(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("NormalizeChannel(%q) expected error, got nil", tc.in)
				}
				return
			}
			if err != nil {
				t.Fatalf("NormalizeChannel(%q) unexpected error: %v", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("NormalizeChannel(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
