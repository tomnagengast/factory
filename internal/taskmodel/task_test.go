package taskmodel

import "testing"

func TestTaskRefNormalize(t *testing.T) {
	tests := []struct {
		name string
		in   TaskRef
		want TaskRef
	}{
		{name: "linear", in: TaskRef{Source: " Linear ", ProviderID: "eng-46", Identifier: "eng-46"}, want: TaskRef{Source: SourceLinear, ProviderID: "ENG-46", Identifier: "ENG-46"}},
		{name: "factory", in: TaskRef{Source: SourceFactory, ProviderID: "task_0123", Identifier: "fac-12"}, want: TaskRef{Source: SourceFactory, ProviderID: "task_0123", Identifier: "FAC-12"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := test.in.Normalize()
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("Normalize() = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestTaskRefRejectsInvalidOrConflictingIdentity(t *testing.T) {
	tests := []TaskRef{
		{},
		{Source: SourceLinear, ProviderID: "ENG-46", Identifier: "ENG-47"},
		{Source: SourceFactory, ProviderID: "ENG-46", Identifier: "FAC-1"},
		{Source: SourceFactory, ProviderID: "task-1", Identifier: "ENG-46"},
	}
	for _, test := range tests {
		if _, err := test.Normalize(); err == nil {
			t.Fatalf("Normalize(%#v) unexpectedly succeeded", test)
		}
	}
}

func TestResolveCompatibilityIdentity(t *testing.T) {
	legacy, err := ResolveCompatibilityIdentity(TaskRef{}, "eng-46")
	if err != nil {
		t.Fatal(err)
	}
	if legacy.Source != SourceLinear || legacy.OwnershipKey() != "linear:ENG-46" {
		t.Fatalf("legacy identity = %#v", legacy)
	}

	if _, err := ResolveCompatibilityIdentity(TaskRef{Source: SourceFactory, ProviderID: "task-1", Identifier: "FAC-1"}, "ENG-46"); err == nil {
		t.Fatal("conflicting current and legacy identities unexpectedly succeeded")
	}
	if factory, err := ResolveCompatibilityIdentity(TaskRef{Source: SourceFactory, ProviderID: "task-1", Identifier: "FAC-1"}, "fac-1"); err != nil || factory.Source != SourceFactory {
		t.Fatalf("Factory compatibility alias = %#v err=%v", factory, err)
	}
}

func TestTaskRefOwnershipSeparatesProviders(t *testing.T) {
	linear := TaskRef{Source: SourceLinear, ProviderID: "FAC-1", Identifier: "FAC-1"}
	factory := TaskRef{Source: SourceFactory, ProviderID: "task-1", Identifier: "FAC-1"}
	if linear.OwnershipKey() == factory.OwnershipKey() {
		t.Fatal("ownership keys collide across providers")
	}
}

func TestTaskRefBranchPrefixSeparatesProviders(t *testing.T) {
	tests := []struct {
		ref  TaskRef
		want string
	}{
		{ref: TaskRef{Source: SourceLinear, ProviderID: "FAC-1", Identifier: "FAC-1"}, want: "fac-1-"},
		{ref: TaskRef{Source: SourceFactory, ProviderID: "task-1", Identifier: "FAC-1"}, want: "factory-task-1-"},
	}
	for _, test := range tests {
		got, err := test.ref.BranchPrefix()
		if err != nil || got != test.want {
			t.Fatalf("BranchPrefix(%#v) = %q, %v; want %q", test.ref, got, err, test.want)
		}
	}
}
