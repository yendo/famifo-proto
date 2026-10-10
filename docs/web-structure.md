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
  GET /tiles       handleTiles ----> parseWindow -> buildTilesView -> tmpl "tiles"
  GET /thumb/{id}  handleThumb ----> lookupMedia -> thumb.Provider
                                                    or serveNoPreview
  GET /full/{id}   handleFull  ----> lookupMedia -> fullViewPath
```

## View models and templates

```
indexView                          view.go
  +-- tilesView (embedded)
  |     +-- []mediaView {ID, PageURL, ThumbURL, FullURL, Date, IsVideo}
  +-- Total, ChunkSize
  +-- DayGroups ([]dayGroup as JSON)
  +-- OpenIndex, AuthEnabled

templates/index.html  {{define "index"}} -> {{template "tiles" .}}
templates/tiles.html  {{define "tiles"}}
```

## Client (static/)

```
index.html
  +-- app.js (entry)
        +-- lightbox.js --+
        +-- scrubber.js --+--> gallery.js --> idiomorph.esm.js
              |                    |
              |                    +--> fetch /tiles?chunk
              |                    |
              +--------------------+--> layout.js

<img>, <video> --> /thumb/{id}, /full/{id}
```

## Dependencies on other packages

```
web --> store, thumb, media, session, imagefmt
web --> Provider (interface; the OIDC implementation is injected)
```
