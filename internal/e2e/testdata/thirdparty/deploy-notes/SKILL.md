---
name: "deploy-notes"
description: "Guides creation of deploy notes and release checklists for production releases"
category: "docs"
source: "external"
checksum: "sha256:ff0c6f0cea83e3e59073262b49530e9343845d6bba96afbadb1eed7bb7f85132"
activation_keywords: ["deploy notes", "release checklist", "deployment", "release notes"]
---
# Deploy Notes Skill

This skill guides the agent when preparing deploy notes for a release.

## Instructions

When the user asks to prepare deploy notes or a release checklist:

1. Collect the list of commits since the last tag (`git log --oneline <last-tag>..HEAD`).
2. Summarize breaking changes, new features, and bug fixes.
3. Verify the checklist:
   - [ ] Changelog updated
   - [ ] Version bumped in Cargo.toml / package.json / go.mod as applicable
   - [ ] Migration steps documented
   - [ ] Rollback plan noted
4. Emit the notes in Markdown under `docs/DEPLOY-NOTES.md` with sections: Summary, Changes, Checklist, Risks.

Keep the output concise and actionable.
