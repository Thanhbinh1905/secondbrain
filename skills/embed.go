// Package skills embeds the agent-facing skill directories that this
// repository tracks.
//
// The embed lives here, beside the files, so the tracked Markdown is the only
// copy. A second copy inside internal/ would be a thing that can silently
// disagree with the thing a contributor edits.
package skills

import "embed"

// SecondBrain holds skills/secondbrain, installed by `brain-axi setup skill`.
//
//go:embed secondbrain
var SecondBrain embed.FS

// SecondBrainRoot is the prefix SecondBrain's paths carry.
const SecondBrainRoot = "secondbrain"
