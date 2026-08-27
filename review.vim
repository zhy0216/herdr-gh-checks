" Loaded into the CI plugin's review diff. `ga` on a line appends
"   path:line  note
" to $CI_NOTES (the PR review file), so the agent gets exact line anchors.
set nomodeline noexrc

function! s:Annotate() abort
  let l:notes = fnamemodify($CI_NOTES, ':p')
  if empty(l:notes) | echo 'no CI_NOTES' | return | endif
  " The Go side creates a private regular file. Refuse a replaced link/path
  " here as a second line of defence before appending user-entered text.
  if getftype(l:notes) !=# 'file' || fnamemodify(resolve(l:notes), ':p') !=# l:notes
    echoerr 'unsafe CI_NOTES path'
    return
  endif
  let l:path = empty($CI_REVIEW_PATH) ? expand('%:.') : $CI_REVIEW_PATH
  let l:path = substitute(l:path, '\\', '/', 'g')
  if l:path =~# '^-' || l:path =~# '^/' || l:path =~# '^[A-Za-z]:[\\/]' || l:path =~# '\(^\|/\)\.\.\(/\|$\)' || l:path =~# '[\x00-\x1f\x7f:]'
    echoerr 'unsafe review path'
    return
  endif
  let l:where = l:path . ':' . line('.')
  let l:note = input('note ' . l:where . '  ')
  if empty(l:note) | return | endif
  call writefile([l:where . '  ' . l:note], l:notes, 'a')
  echo 'noted ' . l:where
endfunction
nnoremap <silent> ga :call <SID>Annotate()<CR>

" Persistent reminder so you don't forget the keys.
if exists('+winbar')
  let &winbar = '%#Comment# ga → annotate this line   ·   :qa → done reviewing'
endif
echo 'review: ga to annotate a line · :qa when done'
