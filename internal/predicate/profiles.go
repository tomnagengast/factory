package predicate

var (
	sdlcDeployProfile = Profile{
		Name: ProfileSDLCDeploy,
		Mode: All,
		Requirements: []Requirement{
			{Atom: DeploymentSuccessful, Failure: "deployment receipt is not successful"},
			{Atom: HealthMatches, Failure: "running health identity does not match the deployment"},
			{Atom: SourceValid, Failure: "completion source is not clean updated main"},
			{Atom: MergeContained, Failure: "updated main does not contain the merge"},
			{Atom: VerifiedHeadContained, Failure: "merged result does not contain the verified head"},
			{Atom: SafeguardsClear, Failure: "pull request checks or reviews regressed"},
			{Atom: RemoteBranchAbsent, Failure: "remote issue branch still exists"},
			{Atom: WorktreeAbsent, Failure: "issue worktree still exists"},
			{Atom: TaskComplete, Failure: "task is not complete"},
			{Atom: ChildrenComplete, Failure: "child work remains incomplete"},
		},
	}
	sdlcRepoOnlyProfile = Profile{
		Name: ProfileSDLCRepoOnly,
		Mode: All,
		Requirements: []Requirement{
			{Atom: SourceValid, Failure: "completion source is not clean updated main"},
			{Atom: MergeContained, Failure: "updated main does not contain the merge"},
			{Atom: VerifiedHeadContained, Failure: "merged result does not contain the verified head"},
			{Atom: SafeguardsClear, Failure: "pull request checks or reviews regressed"},
			{Atom: RemoteBranchAbsent, Failure: "remote issue branch still exists"},
			{Atom: WorktreeAbsent, Failure: "issue worktree still exists"},
			{Atom: TaskComplete, Failure: "task is not complete"},
			{Atom: ChildrenComplete, Failure: "child work remains incomplete"},
		},
	}
)

func SDLCDeployProfile() Profile {
	return cloneProfile(sdlcDeployProfile)
}

func SDLCRepoOnlyProfile() Profile {
	return cloneProfile(sdlcRepoOnlyProfile)
}

func cloneProfile(profile Profile) Profile {
	profile.Requirements = append([]Requirement(nil), profile.Requirements...)
	for index := range profile.Requirements {
		profile.Requirements[index].Parameters = profile.Requirements[index].Parameters.Clone()
	}
	return profile
}
