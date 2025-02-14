# Surgeon - surgically replace dependencies for tests

Surgeon is a library that helps replacing dependencies in a larger object graph
in test code.

The motivating example is you want to test how the application handles an HTTP
request but with dependencies replaced with test a test double; but the
dependencies you want to replace may be deep down the dependency graph; and you
don't want to replace the entire branch manually; and you also want to be able
to refactor.

More information is coming
