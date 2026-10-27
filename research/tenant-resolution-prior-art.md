# Tenant resolution: prior art

Research note for [#2223](https://github.com/gaborage/go-bricks/issues/2223), under the map [#2228](https://github.com/gaborage/go-bricks/issues/2228). It feeds the tenant-resolution design grilling and the unknown-tenant grilling. It records what five frameworks do and does not pick a design.

Every behavior claim below was read from source at a pinned tag, and each quote was re-fetched from its URL before this note was written. A claim taken only from documentation is marked **[docs]**. "Not found in source" means the code was searched and nothing first-party exists.

Sources:

| Framework | Repository | Ref |
| --- | --- | --- |
| ABP Framework | abpframework/abp | `10.7.0` (`c68e856d`) |
| Finbuckle.MultiTenant | Finbuckle/Finbuckle.MultiTenant | `v10.1.5` (`17ccfa0b`) |
| Hibernate ORM / Spring | hibernate/hibernate-orm, spring-projects/{spring-boot,spring-framework,spring-security} | Hibernate `7.4.14`, Boot `v4.1.2`, Framework `v7.0.10`, Security `7.1.2` |
| stancl/tenancy | archtechx/tenancy (the repo moved from stancl/tenancy) | `v3.10.1` (`c4cf5dc6`) |
| django-tenants | django-tenants/django-tenants | `v4.0.1` (`8f933cfe`) |

## GoBricks today (main `0fce4858`)

- The **tenant resolver** is built only from config, `multitenant.resolver.type` (`server/middleware.go:371-396`). `multitenant.TenantResolver` is an exported interface (`multitenant/resolver.go:15-17`), and `server.TenantMiddleware(resolver, skipper)` is exported (`server/tenant_middleware.go:24`), but nothing lets an app put its own resolver in the framework's seat.
- The **tenant seat** is one middleware on the root echo chain (`server/middleware.go:278`). It runs after routing and before group and route middleware, so before per-group authentication (ADR-109). On failure it answers the framework 400 `Invalid tenant` (`server/tenant_middleware.go:53`).
- Every resolved ID must match `^[a-z0-9-]{1,64}$` (`multitenant/tenant_id.go:18`). Nothing checks that the ID names a configured tenant, so a well-formed unknown tenant surfaces as a 500 when a handler first asks for a tenant resource.
- [#2166](https://github.com/gaborage/go-bricks/issues/2166) proposes `TenantOwnedGroup`: a **route family** that skips the seat, resolves its own tenant in group middleware (after its own auth), and has every handler wrapped in a fail-closed guard.

## At a glance

| | Q1 app-supplied strategy | Q2 per route set | Q3 after authentication | Q4 key mapping | Q5 unknown tenant |
| --- | --- | --- | --- | --- | --- |
| ABP | Mutable contributor list | None; a contributor inspects the request | Default: tenancy runs after `UseAuthentication`, user contributor first | GUID → ID, else normalized name; replaceable store/normalizer | Store lookup at the seat → 404 (HTML unless XHR); none → host |
| Finbuckle | `IMultiTenantStrategy` + `WithStrategy` | Exclude-only endpoint metadata | `ClaimStrategy` (authenticates itself); seat runs before auth | Event rewrites the identifier; store | Store lookup; default proceeds with null tenant |
| Hibernate / Spring | Resolver is always app code | None for tenants | Pulled at session open, after Security | Opaque `T`, no normalization | No allowlist; fails lazily on first connection |
| stancl/tenancy | Subclass the middleware or resolver | Native: per-group middleware | None first-party; public `initialize()` | Exact lookup, no hook | DB lookup at the seat → 500 |
| django-tenants | Subclass the middleware | None; tenant picks the urlconf | None; seat must be first | `Domain` row, exact match | DB lookup at the seat → 404 |

## ABP Framework

### Q1. Adding an app-supplied strategy

An app writes an `ITenantResolveContributor` and edits a plain mutable list in `ConfigureServices`: `Add`/`Insert` places it alongside the built-ins, and removing entries replaces them. `TenantResolver` walks the list in order and stops at the first contributor that resolves.

```csharp
    [NotNull]
    public List<ITenantResolveContributor> TenantResolvers { get; }
```

[`AbpTenantResolveOptions.cs` L8-L9](https://github.com/abpframework/abp/blob/10.7.0/framework/src/Volo.Abp.MultiTenancy.Abstractions/Volo/Abp/MultiTenancy/AbpTenantResolveOptions.cs#L8-L9)

```csharp
            foreach (var tenantResolver in Options.TenantResolvers)
            {
                Logger.LogDebug("Trying to resolve tenant through '{TenantResolverName}'...", tenantResolver.Name);
                await tenantResolver.ResolveAsync(context);
```

[`TenantResolver.cs` L35-L38](https://github.com/abpframework/abp/blob/10.7.0/framework/src/Volo.Abp.MultiTenancy/Volo/Abp/MultiTenancy/TenantResolver.cs#L35-L38)

`HttpTenantResolveContributorBase` catches a contributor's exception and logs a warning, so a crashing contributor quietly becomes "no tenant" ([L25-L30](https://github.com/abpframework/abp/blob/10.7.0/framework/src/Volo.Abp.AspNetCore.MultiTenancy/Volo/Abp/AspNetCore/MultiTenancy/HttpTenantResolveContributorBase.cs#L25-L30)).

### Q2. Different strategies per route set

Not found in source. There is one global ordered list; `MultiTenancyMiddleware` calls the resolver with no endpoint or path, and no built-in contributor reads endpoint metadata. "Per route" can only mean a contributor that inspects the request. The built-in route contributor reads the `__tenant` route value, which works because the app template calls `UseRouting()` before `UseMultiTenancy()`.

```csharp
        var tenantId = httpContext.GetRouteValue(context.GetAbpAspNetCoreMultiTenancyOptions().TenantKey);
        return Task.FromResult(tenantId != null ? Convert.ToString(tenantId) : null);
```

[`RouteTenantResolveContributor.cs` L17-L18](https://github.com/abpframework/abp/blob/10.7.0/framework/src/Volo.Abp.AspNetCore.MultiTenancy/Volo/Abp/AspNetCore/MultiTenancy/RouteTenantResolveContributor.cs#L17-L18)

### Q3. Resolving after authentication

ABP resolves after authentication by default. `AbpMultiTenancyModule` inserts `CurrentUserTenantResolveContributor` at position 0, and the app template orders the pipeline `UseRouting` → `UseAuthentication` → `UseMultiTenancy` → `UseAuthorization`.

```csharp
            options.TenantResolvers.Insert(0, new CurrentUserTenantResolveContributor());
```

[`AbpMultiTenancyModule.cs` L35](https://github.com/abpframework/abp/blob/10.7.0/framework/src/Volo.Abp.MultiTenancy/Volo/Abp/MultiTenancy/AbpMultiTenancyModule.cs#L35)

```csharp
        var currentUser = context.ServiceProvider.GetRequiredService<ICurrentUser>();
        if (currentUser.IsAuthenticated)
        {
            context.Handled = true;
            context.TenantIdOrName = currentUser.TenantId?.ToString();
        }
```

[`CurrentUserTenantResolveContributor.cs` L15-L20](https://github.com/abpframework/abp/blob/10.7.0/framework/src/Volo.Abp.MultiTenancy/Volo/Abp/MultiTenancy/CurrentUserTenantResolveContributor.cs#L15-L20)

```csharp
        app.UseAuthentication();

        if (MultiTenancyConsts.IsEnabled)
        {
            app.UseMultiTenancy();
```

[`MyProjectNameHttpApiHostModule.cs` L194-L198](https://github.com/abpframework/abp/blob/10.7.0/templates/app/aspnet-core/src/MyCompanyName.MyProjectName.HttpApi.Host/MyProjectNameHttpApiHostModule.cs#L194-L198)

Two consequences: an authenticated principal always wins, so a header or query value cannot override it; and an authenticated principal with no tenant claim is treated as the host (`Handled = true`, `TenantIdOrName = null`). A client-credential source would be a custom contributor reading `HttpContext.User`.

### Q4. Mapping an external key

The resolved value is an ID-or-name. `TenantConfigurationProvider` looks it up by ID when it parses as a GUID; otherwise it normalizes it (the default normalizer upper-cases) and looks it up by name. A partner code needs a custom contributor that translates before setting `TenantIdOrName`, or a replacement `ITenantStore` or `ITenantNormalizer`.

```csharp
        if (Guid.TryParse(tenantIdOrName, out var parsedTenantId))
        {
            return await TenantStore.FindAsync(parsedTenantId);
        }
        else
        {
            return await TenantStore.FindAsync(TenantNormalizer.NormalizeName(tenantIdOrName)!);
```

[`TenantConfigurationProvider.cs` L69-L75](https://github.com/abpframework/abp/blob/10.7.0/framework/src/Volo.Abp.MultiTenancy/Volo/Abp/MultiTenancy/TenantConfigurationProvider.cs#L69-L75)

### Q5. Unknown tenant, and no tenant

Checked in the middleware at resolution time, by a store lookup. Unknown throws `BusinessException` code `010001`, inactive throws `010002`.

```csharp
            if (tenant == null)
            {
                throw new BusinessException(
                    code: "Volo.AbpIo.MultiTenancy:010001",
                    message: StringLocalizer["TenantNotFoundMessage"],
                    details: StringLocalizer["TenantNotFoundDetails", resolveResult.TenantIdOrName]
```

[`TenantConfigurationProvider.cs` L45-L50](https://github.com/abpframework/abp/blob/10.7.0/framework/src/Volo.Abp.MultiTenancy/Volo/Abp/MultiTenancy/TenantConfigurationProvider.cs#L45-L50)

`MultiTenancyMiddleware` catches it and calls `MultiTenancyMiddlewareErrorPageBuilder`, a replaceable `Func<HttpContext, Exception, Task<bool>>`; when it returns `false` the request continues as the host (fail-open).

```csharp
        catch (Exception e)
        {
            Logger.LogException(e);

            if (await _options.MultiTenancyMiddlewareErrorPageBuilder(context, e))
            {
                return;
            }
        }
```

[`MultiTenancyMiddleware.cs` L49-L57](https://github.com/abpframework/abp/blob/10.7.0/framework/src/Volo.Abp.AspNetCore.MultiTenancy/Volo/Abp/AspNetCore/MultiTenancy/MultiTenancyMiddleware.cs#L49-L57)

The default builder answers **404**: JSON `RemoteServiceErrorResponse` only when `X-Requested-With: XMLHttpRequest`, an HTML page otherwise, and a redirect for a cookie-authenticated non-AJAX GET. It always adds an `Abp-Tenant-Resolve-Error` header, and the details text echoes the submitted value ("There is no tenant with the tenant id or name: {0}").

```csharp
                context.Response.ContentType = resolvedContentType;
                context.Response.StatusCode = (int)HttpStatusCode.NotFound;
```

[`AbpAspNetCoreMultiTenancyOptions.cs` L98-L99](https://github.com/abpframework/abp/blob/10.7.0/framework/src/Volo.Abp.AspNetCore.MultiTenancy/Volo/Abp/AspNetCore/MultiTenancy/AbpAspNetCoreMultiTenancyOptions.cs#L98-L99)

When nothing resolves, ABP raises no error: it applies `FallbackTenant` if set, otherwise proceeds as the host ([`TenantResolver.cs` L51-L60](https://github.com/abpframework/abp/blob/10.7.0/framework/src/Volo.Abp.MultiTenancy/Volo/Abp/MultiTenancy/TenantResolver.cs#L51-L60)).

### Poor fit for the root-chain seat

ABP's first contributor reads the authenticated user, which works only because tenancy runs between authentication and authorization. GoBricks' seat runs before per-group auth, so a claim-based contributor at that seat has nothing to read. ABP treats "no tenant" as the host and carries on, where GoBricks answers 400. Its unknown-tenant check is a store lookup at the seat, which GoBricks' seat does not make today; its default 404 echoes the submitted value, and a custom error handler returning `false` fails open.

## Finbuckle.MultiTenant

### Q1. Adding an app-supplied strategy

An app implements `IMultiTenantStrategy` and registers it with `WithStrategy<T>(…)`, or passes a lambda to `WithDelegateStrategy`. Registrations are additive, and `AddMultiTenant` registers no default strategy, so "instead of the built-ins" means not registering them. The resolver orders strategies by **descending** `Priority`, which contradicts the interface's own doc comment.

```csharp
    Task<string?> GetIdentifierAsync(object context);

    /// <summary>
    /// Strategy execution order priority. Low values are executed first. Equal values are executed in order of registration.
    /// </summary>
    int Priority => 0;
```

[`IMultiTenantStrategy.cs` L16-L21](https://github.com/Finbuckle/Finbuckle.MultiTenant/blob/v10.1.5/src/Finbuckle.MultiTenant.Abstractions/IMultiTenantStrategy.cs#L16-L21)

```csharp
        Strategies = strategies.OrderByDescending(s => s.Priority);
```

[`TenantResolver.cs` L50](https://github.com/Finbuckle/Finbuckle.MultiTenant/blob/v10.1.5/src/Finbuckle.MultiTenant/TenantResolver.cs#L50)

### Q2. Different strategies per route set

No per-endpoint strategy selection exists, and no "require tenant" metadata. Endpoint metadata works one way only: an endpoint can be excluded from resolution. There is also a global `BypassWhen(Func<HttpContext,bool>)`. Otherwise "per route" means a strategy inspects the request (for example `RouteStrategy` reads route values). Because the middleware reads the matched endpoint, routing must run first.

```csharp
        if (context.GetEndpoint()?.Metadata.GetMetadata<IExcludeFromMultiTenantResolutionMetadata>() is
            { ExcludeFromResolution: true })
        {
            await next(context);
            return;
        }
```

[`MultiTenantMiddleware.cs` L49-L54](https://github.com/Finbuckle/Finbuckle.MultiTenant/blob/v10.1.5/src/Finbuckle.MultiTenant.AspNetCore/MultiTenantMiddleware.cs#L49-L54); the builder extension is [`EndpointConventionBuilderExtensions.cs` L23-L25](https://github.com/Finbuckle/Finbuckle.MultiTenant/blob/v10.1.5/src/Finbuckle.MultiTenant.AspNetCore/Extensions/EndpointConventionBuilderExtensions.cs#L23-L25).

### Q3. Resolving after authentication

`ClaimStrategy` reads a claim from an already-authenticated `User`. If the user is not authenticated yet, it instantiates the scheme's handler and runs `AuthenticateAsync` itself, without assigning `HttpContext.User`, so authentication can run twice. No client-credential strategy exists (not found in source).

```csharp
        if (httpContext.User.Identity is { IsAuthenticated: true })
            return httpContext.User.FindFirst(_tenantKey)?.Value;
```

[`ClaimStrategy.cs` L48-L49](https://github.com/Finbuckle/Finbuckle.MultiTenant/blob/v10.1.5/src/Finbuckle.MultiTenant.AspNetCore/Strategies/ClaimStrategy.cs#L48-L49)

```csharp
        var handler =
            (IAuthenticationHandler)ActivatorUtilities.CreateInstance(httpContext.RequestServices,
                authScheme.HandlerType);
        await handler.InitializeAsync(authScheme, httpContext).ConfigureAwait(false);
        httpContext.Items[$"{Constants.TenantToken}__bypass_validate_principal__"] = "true"; // Value doesn't matter.
        var handlerResult = await handler.AuthenticateAsync().ConfigureAwait(false);
```

[`ClaimStrategy.cs` L69-L74](https://github.com/Finbuckle/Finbuckle.MultiTenant/blob/v10.1.5/src/Finbuckle.MultiTenant.AspNetCore/Strategies/ClaimStrategy.cs#L69-L74)

The ordering rule runs the other way from ABP's. **[docs]** `UseMultiTenant()` must come before `UseAuthentication()`, because per-tenant authentication options (challenge scheme, OIDC authority, cookie validation) need the tenant first:

> **Middleware ordering is important.** Place `UseMultiTenant()` **before** any middleware that requires
> per-tenant behavior, such as `UseAuthentication()`, `UseAuthorization()`, and any other components that
> read per-tenant options or services.

[`docs/AspNetCore.md` L53-L55](https://github.com/Finbuckle/Finbuckle.MultiTenant/blob/v10.1.5/docs/AspNetCore.md#L53-L55)

### Q4. Mapping an external key

Strategies return an `Identifier`, and stores look it up by identifier; `Id` is a separate stable key. The hook for rewriting a partner code is the `OnStrategyResolveCompleted` event, whose `Identifier` is settable and read back by the resolver; a custom store is the other route. `InMemoryStore` ignores case unless `IsCaseSensitive` is set.

```csharp
            await _options.CurrentValue.Events.OnStrategyResolveCompleted(strategyResolveCompletedContext)
                .ConfigureAwait(false);
            if (identifier is not null && strategyResolveCompletedContext.Identifier is null && tenantResolverLogger.IsEnabled(LogLevel.Debug))
                tenantResolverLogger.LogDebug("OnStrategyResolveCompleted set non-null Identifier to null");
            identifier = strategyResolveCompletedContext.Identifier;
```

[`TenantResolver.cs` L74-L78](https://github.com/Finbuckle/Finbuckle.MultiTenant/blob/v10.1.5/src/Finbuckle.MultiTenant/TenantResolver.cs#L74-L78)

```csharp
        var stringComparer = StringComparer.OrdinalIgnoreCase;
        if (_options.IsCaseSensitive)
            stringComparer = StringComparer.Ordinal;
```

[`InMemoryStore.cs` L32-L34](https://github.com/Finbuckle/Finbuckle.MultiTenant/blob/v10.1.5/src/Finbuckle.MultiTenant/Stores/InMemoryStore.cs#L32-L34)

### Q5. Unknown tenant, and no tenant

The store lookup is the check. A null from every store leaves `TenantInfo == null`, and the store wrapper also turns a store exception into "not found", so a store outage looks the same as an unknown tenant. By default the middleware does **not** short-circuit: it calls `next` with a null tenant. The opt-in `ShortCircuitWhenTenantNotResolved()` stops the pipeline and redirects only if a `RedirectTo` URI is given; Finbuckle sets no status code and writes no body (a 4xx is not found in source).

```csharp
        if (options?.Predicate is null || !options.Predicate(multiTenantContext))
            await next(context);
        else if (options.RedirectTo is not null)
            context.Response.Redirect(options.RedirectTo.ToString());
```

[`MultiTenantMiddleware.cs` L65-L68](https://github.com/Finbuckle/Finbuckle.MultiTenant/blob/v10.1.5/src/Finbuckle.MultiTenant.AspNetCore/MultiTenantMiddleware.cs#L65-L68)

The short-circuit predicate sees only the final context, so it cannot tell "unknown" from "no identifier". Only `OnStoreResolveCompleted` can: it carries a non-null `Identifier` with `TenantInfo == null` ([`TenantResolver.cs` L100-L109](https://github.com/Finbuckle/Finbuckle.MultiTenant/blob/v10.1.5/src/Finbuckle.MultiTenant/TenantResolver.cs#L100-L109)).

### Poor fit for the root-chain seat

The default (proceed with a null tenant) and the opt-in short circuit (no status, no body) are both weaker than GoBricks' 400. `ClaimStrategy` runs authentication inside the resolver, which inverts the GoBricks layering where per-group auth comes after the seat. Per-route control is exclusion-only metadata plus one global bypass, so it offers no model for choosing a source per route family; it does show that endpoint metadata read by a post-routing seat is enough to exempt an endpoint.

## Spring + Hibernate ORM

Spring Boot ships no HTTP tenant resolution (the Boot `v4.1.2` tree has no file with "tenant" in its path). Tenancy lives in Hibernate's `CurrentTenantIdentifierResolver`; Spring contributes the web-layer ordering and, in Spring Security, a multi-issuer resolver.

### Q1. Adding an app-supplied strategy

The resolver is always application code: Hibernate's `src/main` holds the interface and no implementation. It is registered through the `hibernate.tenant_identifier_resolver` setting (an instance, class or class name; in Boot, a `HibernatePropertiesCustomizer` or `spring.jpa.properties`), or, when that is absent, looked up as a bean from the container (Spring's `HibernateJpaVendorAdapter` installs `SpringBeanContainer`).

```java
		final var currentTenantIdentifierResolver =
				registry.requireService( StrategySelector.class )
						.resolveStrategy( CurrentTenantIdentifierResolver.class,
								settings.get( MULTI_TENANT_IDENTIFIER_RESOLVER ) );
		if ( currentTenantIdentifierResolver == null ) {
			return Helper.getBean(
```

[`MultiTenancy.java` L49-L54](https://github.com/hibernate/hibernate-orm/blob/7.4.14/hibernate-core/src/main/java/org/hibernate/context/spi/MultiTenancy.java#L49-L54)

### Q2. Different strategies per route set

Not first-party. There is one resolver per `SessionFactory`, and its method takes no request argument, so per-route switching is application code (typically a request-scoped holder the resolver reads).

```java
public interface CurrentTenantIdentifierResolver<T> {
	/**
	 * Resolve the current tenant identifier.
	 *
	 * @return The current tenant identifier
	 */
	T resolveCurrentTenantIdentifier();
```

[`CurrentTenantIdentifierResolver.java` L22-L28](https://github.com/hibernate/hibernate-orm/blob/7.4.14/hibernate-core/src/main/java/org/hibernate/context/spi/CurrentTenantIdentifierResolver.java#L22-L28)

Spring Security's per-route selector, `RequestMatcherDelegatingAuthenticationManagerResolver`, picks an `AuthenticationManager` by request matcher, not a tenant ([L84-L88](https://github.com/spring-projects/spring-security/blob/7.1.2/web/src/main/java/org/springframework/security/web/authentication/RequestMatcherDelegatingAuthenticationManagerResolver.java#L84-L88)).

### Q3. Resolving after authentication

Hibernate **pulls** the tenant when a session is built, not per request: the session builder's constructor calls the resolver, and an explicit `.tenantIdentifier(x)` only overrides the value afterward.

```java
	public AbstractCommonBuilder(SessionFactoryImplementor factory) {
		sessionFactory = factory;
		final var options = factory.getSessionFactoryOptions();
		statementInspector = options.getStatementInspector();
		cacheMode = options.getInitialSessionCacheMode();
		connectionHandlingMode = options.getPhysicalConnectionHandlingMode();
		jdbcTimeZone = options.getJdbcTimeZone();
		tenantIdentifier = factory.resolveTenantIdentifier();
	}
```

[`AbstractCommonBuilder.java` L42-L50](https://github.com/hibernate/hibernate-orm/blob/7.4.14/hibernate-core/src/main/java/org/hibernate/engine/creation/internal/AbstractCommonBuilder.java#L42-L50)

In Boot, `spring.jpa.open-in-view` defaults to on and registers `OpenEntityManagerInViewInterceptor` with `addWebRequestInterceptor` ([`JpaBaseConfiguration.java` L236, L264](https://github.com/spring-projects/spring-boot/blob/v4.1.2/module/spring-boot-jpa/src/main/java/org/springframework/boot/jpa/autoconfigure/JpaBaseConfiguration.java#L231-L265)). That is a `DispatcherServlet` interceptor, not a servlet filter, so it runs after the Spring Security filter chain; its `preHandle` opens the EntityManager eagerly, so the resolver runs after authentication and before the controller and can read the `SecurityContext`.

```java
			logger.debug("Opening JPA EntityManager in OpenEntityManagerInViewInterceptor");
			try {
				EntityManager em = createEntityManager();
```

[`OpenEntityManagerInViewInterceptor.java` L86-L88](https://github.com/spring-projects/spring-framework/blob/v7.0.10/spring-orm/src/main/java/org/springframework/orm/jpa/support/OpenEntityManagerInViewInterceptor.java#L86-L88)

### Q4. Mapping an external key

The tenant identifier is opaque and generic (`CurrentTenantIdentifierResolver<T>`); Hibernate neither normalizes nor validates it, so mapping is whatever the app's resolver does. On the Security side, `JwtIssuerAuthenticationManagerResolver` maps a token's `iss` to an `AuthenticationManager` through an exact set, a predicate, or an app-supplied resolver; it yields an authentication manager, not a tenant ID.

```java
	public static JwtIssuerAuthenticationManagerResolver fromTrustedIssuers(Collection<String> trustedIssuers) {
		Assert.notEmpty(trustedIssuers, "trustedIssuers cannot be empty");
		return fromTrustedIssuers(Set.copyOf(trustedIssuers)::contains);
	}
```

[`JwtIssuerAuthenticationManagerResolver.java` L81-L84](https://github.com/spring-projects/spring-security/blob/7.1.2/oauth2/oauth2-resource-server/src/main/java/org/springframework/security/oauth2/server/resource/authentication/JwtIssuerAuthenticationManagerResolver.java#L81-L84)

### Q5. Unknown tenant, and no tenant

Hibernate has no allowlist. A null tenant fails when the session is built:

```java
	private static Object getTenantId( SessionFactoryOptions factoryOptions, SessionCreationOptions options ) {
		final Object tenantIdentifier = options.getTenantIdentifierValue();
		if ( factoryOptions.isMultiTenancyEnabled() && tenantIdentifier == null ) {
			throw new HibernateException( "SessionFactory configured for multi-tenancy, but no tenant identifier specified" );
```

[`AbstractSharedSessionContract.java` L361-L364](https://github.com/hibernate/hibernate-orm/blob/7.4.14/hibernate-core/src/main/java/org/hibernate/internal/AbstractSharedSessionContract.java#L361-L364)

A well-formed unknown tenant is the app's `MultiTenantConnectionProvider`'s business, and it fails lazily, when the first JDBC connection is acquired. The only shipped data-source implementation concatenates the raw ID into a JNDI name and lets the lookup fail:

```java
		DataSource dataSource = dataSourceMap().get( tenantIdentifier );
		if ( dataSource == null ) {
			dataSource = (DataSource) jndiService.locate( baseJndiNamespace + '/' + tenantIdentifier );
```

[`DataSourceBasedMultiTenantConnectionProviderImpl.java` L59-L61](https://github.com/hibernate/hibernate-orm/blob/7.4.14/hibernate-core/src/main/java/org/hibernate/engine/jdbc/connections/spi/DataSourceBasedMultiTenantConnectionProviderImpl.java#L59-L61)

No HTTP status for either case appears in source; an unmapped exception reaching Spring MVC is presumably a 500, which is inference. The one up-front rejection is Spring Security's: an untrusted issuer throws `InvalidBearerTokenException("Invalid issuer")`, which the bearer entry point answers with **401** and `WWW-Authenticate`.

```java
			AuthenticationManager authenticationManager = this.issuerAuthenticationManagerResolver.resolve(issuer);
			if (authenticationManager == null) {
				AuthenticationException ex = new InvalidBearerTokenException("Invalid issuer");
```

[`JwtIssuerAuthenticationManagerResolver.java` L151-L153](https://github.com/spring-projects/spring-security/blob/7.1.2/oauth2/oauth2-resource-server/src/main/java/org/springframework/security/oauth2/server/resource/authentication/JwtIssuerAuthenticationManagerResolver.java#L151-L153)

### Poor fit for the root-chain seat

Hibernate's model is a code seam with no built-in HTTP strategies, the inverse of GoBricks' config-built strategies with no code seam. Its tenant is pulled late, inside `DispatcherServlet` after Security, so a verified claim is naturally available; GoBricks' seat runs before group auth, so adopting the model means moving or deferring the seat, not just adding a strategy. Like GoBricks, it surfaces a well-formed unknown tenant late, on first data access.

## stancl/tenancy (Laravel)

### Q1. Adding an app-supplied strategy

An app subclasses the abstract `IdentificationMiddleware` and calls `initializeTenancy`, backed by a class implementing `Contracts\TenantResolver::resolve(...$args): Tenant`. Each built-in middleware type-hints its own concrete resolver, so swapping the resolver behind a built-in means subclassing the middleware or rebinding the resolver in the container. **[docs]** The docs only say "you're free to write additional tenant resolvers".

```php
    public function initializeTenancy($request, $next, ...$resolverArguments)
    {
        try {
            $this->tenancy->initialize(
                $this->resolver->resolve(...$resolverArguments)
            );
```

[`IdentificationMiddleware.php` L22-L27](https://github.com/archtechx/tenancy/blob/v3.10.1/src/Middleware/IdentificationMiddleware.php#L22-L27)

### Q2. Different strategies per route set

Yes, natively. Identification is ordinary Laravel route middleware attached per route group, never globally; the package's service provider registers no middleware, and the published route stub attaches it to a group. Each group can use a different `InitializeTenancyBy*` class (`Domain`, `Subdomain`, `Path`, `RequestData`).

```php
Route::middleware([
    'web',
    InitializeTenancyByDomain::class,
    PreventAccessFromCentralDomains::class,
])->group(function () {
```

[`assets/tenant_routes.stub.php` L21-L25](https://github.com/archtechx/tenancy/blob/v3.10.1/assets/tenant_routes.stub.php#L21-L25)

The `UniversalRoutes` feature lets a route that carries a `universal` marker group fall through to the central (no-tenant) context when domain identification fails:

```php
            $middleware::$onFail = function ($exception, $request, $next) {
                if (static::routeHasMiddleware($request->route(), static::$middlewareGroup)) {
                    return $next($request);
                }

                throw $exception;
```

[`UniversalRoutes.php` L26-L31](https://github.com/archtechx/tenancy/blob/v3.10.1/src/Features/UniversalRoutes.php#L26-L31)

### Q3. Resolving after authentication

Not found in source: no first-party identification from a token claim or client credential. Because `tenancy()->initialize()` accepts a bare ID and is public, an app can write its own middleware, list it after `auth:…` in a group, and call it. The "identification before auth" default comes from the published, app-owned service provider stub, which puts the six built-in classes at the front of Laravel's middleware priority list (and so ahead of `StartSession` and `AuthenticatesRequests`). A direct `IdentificationMiddleware` subclass has no priority entry and keeps its listed position.

```php
        foreach (array_reverse($tenancyMiddleware) as $middleware) {
            $this->app[\Illuminate\Contracts\Http\Kernel::class]->prependToMiddlewarePriority($middleware);
        }
```

[`assets/TenancyServiceProvider.stub.php` L144-L146](https://github.com/archtechx/tenancy/blob/v3.10.1/assets/TenancyServiceProvider.stub.php#L144-L146)

```php
    public function initialize($tenant): void
    {
        if (! is_object($tenant)) {
            $tenantId = $tenant;
            $tenant = $this->find($tenantId);

            if (! $tenant) {
                throw new TenantCouldNotBeIdentifiedById($tenantId);
```

[`Tenancy.php` L32-L39](https://github.com/archtechx/tenancy/blob/v3.10.1/src/Tenancy.php#L32-L39)

### Q4. Mapping an external key

Path and request-data identification call `tenancy()->find($value)`, an exact `where(<tenant key>, $id)->first()`; domain and subdomain look up a `domains` table. The package normalizes nothing (case behavior is the database collation's). There is no dedicated map hook; the extension points are the protected `getPayload()` on `InitializeTenancyByRequestData`, a resolver subclass plus container rebind, or a tenant model overriding `getTenantKeyName()`.

```php
    public function find($id): ?Tenant
    {
        return $this->model()->where($this->model()->getTenantKeyName(), $id)->first();
    }
```

[`Tenancy.php` L101-L104](https://github.com/archtechx/tenancy/blob/v3.10.1/src/Tenancy.php#L101-L104)

### Q5. Unknown tenant, and no tenant

Checked inside the identification middleware by the database lookup (in effect an allowlist). The middleware catches `TenantCouldNotBeIdentifiedException` and hands it to a static `$onFail` callback whose default rethrows. `$onFail` is redeclared on each concrete middleware class, so it is set per class.

```php
        } catch (TenantCouldNotBeIdentifiedException $e) {
            $onFail = static::$onFail ?? function ($e) {
                throw $e;
            };

            return $onFail($e, $request, $next);
```

[`IdentificationMiddleware.php` L28-L33](https://github.com/archtechx/tenancy/blob/v3.10.1/src/Middleware/IdentificationMiddleware.php#L28-L33)

The exceptions extend plain `Exception` and define no `render()` or status ([`TenantCouldNotBeIdentifiedException.php` L9-L11](https://github.com/archtechx/tenancy/blob/v3.10.1/src/Contracts/TenantCouldNotBeIdentifiedException.php#L9-L11)), so Laravel's handler wraps them as a **500** (`{"message":"Server Error"}` with debug off):

```php
        if (! $this->isHttpException($e)) {
            $e = new HttpException(500, $e->getMessage(), $e);
        }
```

[laravel/framework `Handler.php` L956-L958](https://github.com/laravel/framework/blob/v13.38.0/src/Illuminate/Foundation/Exceptions/Handler.php#L956-L958)

A missing header and query parameter yields a null payload, the same exception, and the same 500. `PreventAccessFromCentralDomains` answers 404 on a central host.

### Poor fit for the root-chain seat

stancl is the direct precedent for per-route-family identification: identification is group middleware, each family picks its class, and a public `initialize()` seam lets a post-auth middleware set the tenant. That is the opposite of a single root-chain seat. Its extension mechanism is mutable PHP statics (`$onFail`, `$header`, `$tenantParameterName`) plus container rebinding, which cuts against GoBricks' explicit, config-built model, and its existence check assumes a tenant table.

## django-tenants

### Q1. Adding an app-supplied strategy

Only in code: subclass `TenantMainMiddleware` and override `hostname_from_request` (extract the key), `get_tenant` (map it) or `no_tenant_found` (handle a miss), then list the subclass in `MIDDLEWARE`. The shipped `SuspiciousTenantMiddleware` and `DefaultTenantMiddleware` are built this way. No setting names a resolver.

```python
    def hostname_from_request(request):
        """ Extracts hostname from request. Used for custom requests filtering.
            By default removes the request's port and common prefixes.
        """
        return remove_www(request.get_host().split(':')[0])
```

[`middleware/main.py` L22-L26](https://github.com/django-tenants/django-tenants/blob/v4.0.1/django_tenants/middleware/main.py#L22-L26); subclass example: [`middleware/default.py` L10-L25](https://github.com/django-tenants/django-tenants/blob/v4.0.1/django_tenants/middleware/default.py#L10-L25)

### Q2. Different strategies per route set

Not found in source. Resolution is one global middleware, and the tenant selects the urlconf afterward (`PUBLIC_SCHEMA_URLCONF`, per-type URLCONF, or the subfolder mount): the tenant picks the routes, never the reverse. The only first-party way to chain strategies is that `TenantSubfolderMiddleware` steps aside when a tenant is already set:

```python
        # Short circuit if tenant is already set by another middleware.
        # This allows for multiple tenant-resolving middleware chained together.
        if hasattr(request, "tenant"):
            return
```

[`middleware/subfolder.py` L34-L37](https://github.com/django-tenants/django-tenants/blob/v4.0.1/django_tenants/middleware/subfolder.py#L34-L37)

### Q3. Resolving after authentication

Not found in source: nothing in `django_tenants/` reads a token, a claim, `Authorization` or `request.user`. The middleware must sit at the top of `MIDDLEWARE` because it switches the database schema, and the session and user tables that later middleware query live in that schema. (Django REST framework authenticates inside the view, after all middleware, so a DRF token is not available at resolution time.)

> Add the middleware ``django_tenants.middleware.main.TenantMainMiddleware`` to the top of ``MIDDLEWARE``, so that each request can be set to use the correct schema.

**[docs]** [`docs/install.rst` L28](https://github.com/django-tenants/django-tenants/blob/v4.0.1/docs/install.rst#L28)

### Q4. Mapping an external key

The mapping is a database row: `Domain.domain` (unique) with a foreign key to the tenant, looked up by exact match. Normalization is only port stripping and a leading `www.`; django-tenants does not lowercase. Subfolder mode looks up the same `domain` field with the first path segment after the prefix.

```python
    def get_tenant(self, domain_model, hostname):
        domain = domain_model.objects.select_related('tenant').get(domain=hostname)
        return domain.tenant
```

[`middleware/main.py` L28-L30](https://github.com/django-tenants/django-tenants/blob/v4.0.1/django_tenants/middleware/main.py#L28-L30)

### Q5. Unknown tenant, and no tenant

Checked at resolution time: the `Domain` query's `DoesNotExist` reaches `no_tenant_found`, so the domain table is the allowlist. In order of precedence: `DEFAULT_NOT_FOUND_TENANT_VIEW` returns whatever that view returns; else `SHOW_PUBLIC_IF_NO_TENANT_FOUND` continues on the public schema; else it raises `TENANT_NOT_FOUND_EXCEPTION`, a class attribute defaulting to `Http404` (Django's stock **404** page). `SuspiciousTenantMiddleware` raises `DisallowedHost` instead (a 400); `DefaultTenantMiddleware` never fails and falls back to a default schema. The subfolder middleware always raises `Http404` for an unknown segment and ignores both settings.

```python
        elif hasattr(settings, 'SHOW_PUBLIC_IF_NO_TENANT_FOUND') and settings.SHOW_PUBLIC_IF_NO_TENANT_FOUND:
            self.setup_url_routing(request=request, force_public=True)
        else:
            raise self.TENANT_NOT_FOUND_EXCEPTION('No tenant for hostname "%s"' % hostname)
```

[`middleware/main.py` L68-L71](https://github.com/django-tenants/django-tenants/blob/v4.0.1/django_tenants/middleware/main.py#L68-L71); subfolder: [`middleware/subfolder.py` L83-L86](https://github.com/django-tenants/django-tenants/blob/v4.0.1/django_tenants/middleware/subfolder.py#L83-L86)

In host mode there is no "missing key" case: the public tenant is just another `Domain` row. In subfolder mode a request without the prefix is served by the public tenant.

### Poor fit for the root-chain seat

django-tenants mirrors GoBricks most closely: one global seat, before any authentication, with no first-party post-auth source. It differs in having a code seam (subclass and override three methods) and in failing an unknown tenant at the seat with a 404 indistinguishable from an unknown route, at the cost of a registry lookup per request. Its public and default-schema fallbacks silently serve a tenant the caller did not name.

## Cross-cutting findings

- **Every surveyed framework exposes a code seam; none is config-only.** ABP's contributor list, Finbuckle's strategies, Hibernate's resolver, stancl's middleware and resolver classes, and django-tenants' overridable middleware methods are all application code. The config-level knobs that do exist (ABP `FallbackTenant`, Finbuckle `IsCaseSensitive`, django `SHOW_PUBLIC_IF_NO_TENANT_FOUND` / `DEFAULT_NOT_FOUND_TENANT_VIEW`) tune a built-in strategy or its failure path; none adds a source.
- **Only stancl selects the tenant source per route set**, and it does so by not having a global seat: identification is route-group middleware. Finbuckle can only exclude an endpoint from its global seat, keyed on the matched endpoint's metadata. ABP and django-tenants have one global seat; Hibernate has one resolver per `SessionFactory`.
- **Post-auth resolution exists only where the seat runs after authentication.** ABP orders `UseAuthentication` before `UseMultiTenancy`; Spring Boot's open-in-view pulls the tenant inside `DispatcherServlet`, after the Security filter chain. Finbuckle keeps its seat before authentication and gets a claim by running authentication inside `ClaimStrategy`. stancl and django-tenants ship no post-auth source; stancl's public `initialize()` lets an app write one after `auth:` in a group.
- **Where unknown tenants are caught, an existence lookup catches them, at the seat.** ABP (404), stancl (500) and django-tenants (404) look the identifier up in a store or table during resolution. Finbuckle looks it up but proceeds with a null tenant by default. Hibernate, like GoBricks today, surfaces it lazily on first data access. None of the quoted resolution paths applies a format grammar like `^[a-z0-9-]{1,64}$`; the Hibernate JNDI provider concatenates the raw ID into a lookup name.
- **Failure statuses disagree**: 404 (ABP, django-tenants), 500 (stancl), 401 for an untrusted issuer (Spring Security), 400 (django-tenants' `SuspiciousTenantMiddleware`; GoBricks today), or no response at all (Finbuckle's default). ABP's 404 body echoes the submitted value.

## Implications for GoBricks

This section maps the patterns onto the candidate shapes for the design grilling. It does not choose one.

- **(A) Route-family ownership** ([#2166](https://github.com/gaborage/go-bricks/issues/2166)).
  - stancl is the direct precedent for per-family sources (need d) and for a post-auth source (need c) placed after the family's `auth:` middleware, with a public setter (`initialize()`) analogous to `multitenant.SetTenant`. It has no root seat to exempt from, so the "skip the seat" half of #2166 has no analog there; stancl is opt-in per group where GoBricks would be opt-out.
  - Finbuckle's exclusion metadata is the "skip the seat" half: a post-routing seat reads the matched endpoint and steps aside. It is keyed on the matched endpoint, not the URL, as #2166's `c.Path()` key is. Finbuckle has no counterpart to #2166's fail-closed guard: an excluded endpoint simply runs without a tenant.
  - stancl's `UniversalRoutes` (fall through to a no-tenant context on failure) is the opposite of a fail-closed guard and shows the risk the guard exists for.
- **(B) App-wide code-supplied resolver at the seat.**
  - ABP, Finbuckle, django-tenants and Hibernate all use this shape, and it covers key mapping (need b): ABP's normalizer and store, Finbuckle's `OnStrategyResolveCompleted`, django-tenants' `get_tenant` override.
  - It reaches a post-auth source (need c) only when the seat runs after authentication (ABP, Spring) or when the resolver authenticates itself (Finbuckle `ClaimStrategy`, which can authenticate twice). GoBricks' seat runs before per-group auth (ADR-109), so B alone does not meet (c) without moving the seat or having the resolver verify credentials itself.
  - B alone also does not give per-family sources (need d), except by a resolver that branches on the request, which is how ABP's route contributor and Finbuckle's `RouteStrategy` work.
  - The frameworks register resolvers at DI configuration time and let them pull dependencies from the container per request (for example ABP's user contributor resolves `ICurrentUser` from `context.ServiceProvider`). GoBricks would bind after module `Init` to reach dependencies built there, the timing argument ADR-036 made.
- **(C) Both.** Finbuckle is the nearest single framework: global code strategies plus per-endpoint exclusion. stancl gets both from one mechanism, since each group's identification middleware can carry its own custom resolver class. In GoBricks terms, B would serve (b) on routes that keep the seat, and A would serve (c) and (d) on families that own resolution.
- **(D) Config-only extensions.** No surveyed framework offers a config-only way to add a source, and none reaches a post-auth source (need c) without code. What is expressed as configuration elsewhere is narrower: key normalization (ABP upper-cases names, Finbuckle's in-memory store ignores case by default), a tenant registry consulted at the seat (ABP's store, Finbuckle's configuration store, django-tenants' `Domain` table), and the not-found response (django-tenants' `DEFAULT_NOT_FOUND_TENANT_VIEW`, ABP's replaceable error builder). Those map onto (b) and the unknown-tenant question, not onto (c) or (d).
- **For the unknown-tenant grilling.** Checking existence at the seat is the majority pattern, at the cost of a lookup per request; the statuses chosen disagree, and three of the five can fail open: two by configuration (ABP's `false` return, django-tenants' public fallback) and one by default (Finbuckle). A route-family design (A) also has to decide whether the family's resolver or the guard owns that check.
