# SPDX-License-Identifier: Apache-2.0
"""Allow ``python -m sbx`` as an alternative to the sbx command."""

import sys

from sbx.cli import main

sys.exit(main())
