# Policy

Rules match a public namespaced tool exactly or with a trailing `.*`. Evaluation
is deny first, then allow; tools not allowed are hidden and unavailable.

Routes are stored at catalog publication time. Conduit never reconstructs a
route by splitting a public name.
