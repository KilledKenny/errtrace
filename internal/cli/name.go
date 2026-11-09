package cli

// BinaryName is the command's name. It is the only place the name is
// spelled out: the usage text, the "<BinaryName>: " prefix of every error
// message and the default cache directory all use it, so renaming the tool
// means changing this constant and the cmd directory.
const BinaryName = "errtrace"
