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
  GET /item/{id}   handleItem  --+-> renderIndex -> buildIndexView -> tmpl "index"
  GET /tiles       handleTiles ----> parseChunk -> listMedia -> JSON
  GET /thumb/{id}  handleThumb ----> lookupMedia -> thumb.Provider
                                                    or serveNoPreview
  GET /full/{id}   handleFull  ----> lookupMedia -> fullViewPath
```

## View models and templates

```
indexView                          view.go
  +-- Total, ChunkSize
  +-- DayGroups ([]dayGroup as JSON)
  +-- OpenIndex, AuthEnabled

[]mediaView (JSON from /tiles)     view.go
  {id, pageUrl, fullUrl, thumbUrl, date, isVideo}

templates/index.html  {{define "index"}}
```

## Client (static/)

```
index.html
  +-- app.js (entry)
        +-- gallery.js  --> chunk.js, layout.js, idiomorph.esm.js
        +-- lightbox.js --> gallery.js, chunk.js
        +-- scrubber.js --> gallery.js, chunk.js, layout.js

chunk.js --> fetch /tiles?chunk

<img>, <video> --> /thumb/{id}, /full/{id}
```

## Dependencies on other packages

```
web --> store, thumb, media, session, imagefmt
web --> Provider (interface; the OIDC implementation is injected)
```
