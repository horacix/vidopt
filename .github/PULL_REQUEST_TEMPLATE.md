## Description
Briefly describe the change and the rationale behind it.

## Type of Change
- [ ] Bug fix (non-breaking change which fixes an issue)
- [ ] New feature (non-breaking change which adds functionality)
- [ ] Performance optimization
- [ ] Refactoring / Code cleanup
- [ ] Documentation update

## Related Issues
Closes # (issue number)

## Testing Performed
Describe the tests you ran to verify your changes:
- [ ] Unit tests pass (`go test -v ./...`)
- [ ] Tested with mock or real media file via `vidopt probe` / `vidopt process`
- [ ] Tested hardware acceleration (`vidopt hwcheck`) if hardware code was touched
- [ ] Code formatted with `gofmt -s -w .`

## Checklist
- [ ] My code follows the project invariants (RAID scratch isolation, pure-Go SQLite, lossless audio/subs).
- [ ] I have added tests that prove my fix is effective or that my feature works.
- [ ] I have updated relevant documentation (`README.md`, `config.example.yaml`, or `AGENTS.md`).
