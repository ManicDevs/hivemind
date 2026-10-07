// environment is a separate, self-contained module so that host probing cannot
// be pulled in transitively by someone who only wants file analysis. Importing
// bininspect/environment is therefore an explicit, auditable statement that the
// caller accepts live host inspection.
module github.com/hivemind/bininspect/environment

go 1.26
