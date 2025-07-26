# Surgeon - surgically replace dependencies for tests

Surgeon is a library that helps replacing dependencies in a larger object graph
in test code.

The motivating example is you want to test how the application handles an HTTP
request but with dependencies replaced with test a test double; but the
dependencies you want to replace may be deep down the dependency graph; and you
don't want to replace the entire branch manually; and you also want to be able
to refactor.


## Principle

During startup, you may have a larger object graph of the dependencies in the system. E.g., for a pure HTTP server, most components would be a dependency to the root server component.

![Illustration showing a larger object graph](https://github.com/user-attachments/assets/4a3b3f75-8c1f-4293-8caf-7872f380f034)

For verifying specific features, authentication in this example, you may want to
replace the `Authenticator` with a mock implementation, and a `SessionStore`
with an in-memory session store. But all the objects in the other branches of
the graph can be reused:

![Illustration showing a larger object graph where some objects have been replaced, while branches are reused](https://github.com/user-attachments/assets/5d30eee3-5c41-4b27-982b-e06d4eb456ee)

Surgeon will allow you to accomplish this. You provide an already initialised
graph, it will analyse each component's direct and indirect dependencies.

You only need to do this once, e.g. an `init` function in the test suite.

Once this is build, you can efficiently replace a dependency with a test double.
The original dependency analysis permits to only replace any branches in the
graph that has a dependency to replaced component. Each replacement creates a
new modified graph; which is why the original graph is safe to reuse.

You can also create shared modified graphs with common replacements; such as
replacing a session store with an in-memory session store.

## Caveat

A problem is that some components in the graph may require initialisation code
executed at startup, so a simple clone is flawed. For example http routes will
require some initialization. E.g.:

```go
type RootRouter struct {
    *http.ServeMux
    ProfileRouter ProfileRouter
    AuthRouter    AuthRouter
}

func NewRootRouter() *RootRouter {
    result := &RootRouter {
        http.NewServeMux(),
        NewProfileRouter(),
        NewAuthRouter(),
    }
    result.Handle("/profile/", result.ProfileRouter)
    result.Handle("/auth/", result.AuthRouter)
    return result;
}

var RootRouter = NewRootRouter()

// The authentication dependes on an abstraction for validating credentials
type Authenticator interface {
    Authenticate(string, string) (Account, bool)
}

type AuthRouter struct {
    *http.ServeMux
    Authenticator Authenticator
}

func NewAuthRouter() *AuthRouter {
    result = &AuthRouter(
}
```

When surgeon replaces the `Authenticator` it creates a copy of the `AuthRouter`
and `RootRouter`. But the initialised routes reference the original routes.

### Solution - extract initialization to an `Init` function

Surgeon checks for the presence of an interface `Initier`, and calls `Init()` on
all the objects that it clones.

By moving initialization code to an `Init()` function, surgeon can reinitialise
all cloned objects in the graph.

```go
type Initier struct {
    Init()
}
```

With that, we can change the initialization code:

```go
func NewRootRouter() *RootRouter {
    result := &RootRouter {
        NewProfileRouter(),
        NewAuthRouter(),
    }
    result.Init()
    return result
}

func (r *RootRouter) Init() {
    r.ServeMux = http.NewServeMux()
    r.ServeMux.Handle("/profile/", result.ProfileRouter)
    r.ServeMux.Handle("/auth/", result.AuthRouter)
}
```

And similar for the `AuthRouter`.

Surgeon will still only call `Init` on the few objects in the graph that it
actually clones.

### Reinitialization will run in the correct order

Surgeon will always call `Init` on the dependencies before the dependee. Or in
other words, the `Init` function can safely assume that dependencies have
allready been initialised when being cloned by surgeon.

### Is this a good solution?

This approach has a problem that is a specialisation of a general type of
problems: The developer _must have the knowledge_ that a specific practice must
be followed, and the developer _must remember_ to follow this practice. And
there is no compiler support to help this.

For larger teams with new members arriving, this can easily become difficult to
adhere to.
