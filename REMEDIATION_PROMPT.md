# Technical Task & Prompt: Fix Security, Boundary, and Quality Findings in Jamshid Backend

**Instructions:** Copy and pass the prompt below to your agent or engineering workflow when you are ready to implement the fixes. **Do not implement yet.**

---

```markdown
You are an expert Go systems engineer following hexagonal architecture, DDD, and Go security best practices.

### Objective
Remediate the 4 security, boundary, and input-validation findings identified during the Jamshid backend audit without altering existing public API contracts or breaking existing tests.

### Context & Files to Modify
- `backend/internal/domain/user/user.go`
- `backend/internal/adapters/inbound/http/user_handler.go`
- `backend/internal/adapters/inbound/http/handler.go`
- `backend/internal/adapters/inbound/http/operations.go`
- `backend/internal/adapters/outbound/postgres/egress.go`
- Relevant unit tests in `user_test.go`, `user_handler_test.go`, and `egress_test.go`.

---

### Tasks & Technical Specifications

#### 1. User Input Length Limits & Bcrypt DoS Protection
In `internal/domain/user/user.go`:
- Update `ValidateIdentifier`:
  - Enforce maximum length of 254 characters (standard email/username limit).
  - Return `ErrInvalidIdentifier` if `len(normalized) < 3 || len(normalized) > 254`.
- Update `ValidatePassword`:
  - Enforce maximum length of 72 characters (bcrypt truncates passwords at 72 bytes; accepting longer strings wastes hashing CPU and gives a false security illusion).
  - Return `ErrInvalidPassword` if `len(password) < 6 || len(password) > 72`.
- In `internal/adapters/inbound/http/user_handler.go`:
  - Ensure clear Persian error messages for max length violations:
    - Identifier: `نام کاربری یا ایمیل باید بین ۳ تا ۲۵۴ نویسه باشد`
    - Password: `رمز عبور باید بین ۶ تا ۷۲ نویسه باشد`

#### 2. Pagination Clamping in Saved Listings
In `internal/adapters/inbound/http/user_handler.go` (`GetSavedListings`):
- Parse `limit` and `offset` defensively:
  - Default `limit` = 50.
  - If `limit <= 0` or `limit > 100`, clamp to 50.
  - If `offset < 0`, clamp to 0.

#### 3. Egress Proxy SSRF / Loopback Protection
In `internal/adapters/outbound/postgres/egress.go` (`ValidateProxy`):
- Add SSRF protection to `ValidateProxy(endpoint string) error`:
  - Parse the endpoint host using `net.SplitHostPort` or `u.Hostname()`.
  - Disallow `localhost` and loopback IP ranges (`127.0.0.0/8`, `::1`).
  - Disallow cloud metadata link-local range (`169.254.0.0/16`, `fe80::/10`).
  - If invalid or targeting loopback/metadata, return `errors.New("proxy address cannot target loopback or metadata services")`.

#### 4. Hexagonal Boundary Decoupling in HTTP Handlers
- In `internal/adapters/inbound/http/handler.go`:
  - Replace direct instantiation of `scrapers.NewCategoryCatalog(...)` and `scrapers.DefaultProviderCatalog()`.
  - Introduce consumer-owned interface `CategoryCatalogProvider` in `handler.go` or pass it via constructor:
    ```go
    type CategoryCatalogProvider interface {
        GetCategories(ctx context.Context, vType vehicle.VehicleType, parentID string) ([]vehicle.Category, error)
    }
    ```
- In `internal/adapters/inbound/http/operations.go`:
  - Replace concrete `store *postgres.Store` parameter in `ConfigureOperations` with a focused consumer interface:
    ```go
    type OperationsStore interface {
        Coverage(ctx context.Context) ([]CoverageRecord, error)
        Proxies(ctx context.Context) ([]EgressProxy, error)
        PutProxy(ctx context.Context, id, endpoint string, enabled bool) (string, error)
        DeleteProxy(ctx context.Context, id string) error
    }
    ```

---

### Acceptance Criteria & Verification
1. `go test -v -race ./...` passes with 0 failures and no race conditions.
2. `go vet ./...` reports 0 issues.
3. Unit tests added for:
   - Long identifier (> 254 chars) and long password (> 72 chars).
   - Saved listings query with `limit=5000` clamped to 50.
   - `ValidateProxy("http://127.0.0.1:8080")` and `ValidateProxy("http://169.254.169.254:80")` rejected.
4. `make build` compiles `bin/jamshid-backend` successfully.
5. All existing frontend tests pass (`npx tsx --test src/utils/*.test.ts src/services/*.test.ts src/components/*.test.tsx`).
```
