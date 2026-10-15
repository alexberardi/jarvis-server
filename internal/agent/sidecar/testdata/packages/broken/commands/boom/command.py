"""An installed package whose command can't import."""

import a_module_that_is_not_installed  # noqa: F401
