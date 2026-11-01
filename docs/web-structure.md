# Structure of internal/web

## Server (Go)

```
NewHandler(store, thumbs, auth, log)                      web.go
  |
  v
securityHeaders (CSP, nosniff)
  |
  +-- GET /static/*  -> FileServerFS(embed static/)   [no auth, no session]
  |
  +-- /
       +-- auth == nil -> routes
       |
       +-- auth != nil -> sessions.LoadAndSave
                            |
                            +-- Auth.addRoutes                 login.go
                            |     GET  /auth/login     handleLogin
                            |     GET  /auth/callback  handleCallback
                            |     POST /logout         handleLogout
                            |     GET  /signed-out     handleSignedOut
                            |
                            +-- / -> requireSignIn -> routes
                                     (isDataPath picks the reply)

routes (methods of Handler)                               web.go
  GET /{$}         handleIndex --+
  GET /item/{id}   handleItem  --+-> filterFrom -> renderIndex -> buildIndexView -> tmpl "index"
  GET /tiles       handleTiles ----> parseChunk, filterFrom -> listMedia -> JSON
  GET /thumb/{id}  handleThumb ----> lookupMedia -> thumb.Provider
                                                    or serveNoPreview
  GET /full/{id}   handleFull  ----> lookupMedia -> fullViewPath
  GET /info/{id}   handleInfo  ----> lookupMedia -> owner.Name -> JSON

filter.go
  filterFrom (?owner= -> store.Filter), filterURL (store.Filter -> query),
  ownerChoices (store.Owners -> owner.Name)
```

## View models and templates

```
indexView                          view.go
  +-- Total, ChunkSize
  +-- TilesURL, HomeURL (carry the filter)
  +-- DayGroups ([]dayGroup as JSON)
  +-- OpenIndex, AuthEnabled
  +-- Filtered, Owners ([]ownerChoice)

[]mediaView (JSON from /tiles)     view.go
  {id, pageUrl, fullUrl, thumbUrl, infoUrl, date, isVideo}
  pageUrl carries the filter (/item/{id}?owner=...)

infoView (JSON from /info/{id})    view.go
  {owner?, takenAt}

templates/index.html  {{define "index"}}
```

## Client (static/)

```
index.html
  +-- app.js (entry)
        +-- gallery.js  --> chunk.js, datefmt.js, layout.js, idiomorph.esm.js
        +-- lightbox.js --> gallery.js, chunk.js, datefmt.js
        +-- scrubber.js --> gallery.js, chunk.js, layout.js
        +-- filters.js  (submits the #filters form on change;
                         opens it as a sheet on narrow screens)

chunk.js --> fetch data-tiles + chunk
lightbox.js --> fetch /info/{id}; closes to data-home

<img>, <video> --> /thumb/{id}, /full/{id}
```

## Dependencies on other packages

```
web --> store, thumb, media, owner, session, imagefmt
web --> Provider (interface; the OIDC implementation is injected)
```
