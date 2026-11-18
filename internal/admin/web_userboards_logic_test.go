//go:build uitest

// Behaviour of the main-section mark in the user dialog and of the no-section notice, run under node.
// Requires node; run:
//
//	go test -tags=uitest ./internal/admin/ -run 'TestUserBoardsLogic'
package admin

import "testing"

// TestUserBoardsLogic runs userBoardAccess, the dialog body read by readUserForm, syncUserBoards and boardsNone
// on stub data: an old record keeps main, membership names follow the section order, an administrator sees
// every section, the cleared mark is sent only for a USER and the notice shows only for a user with no section.
func TestUserBoardsLogic(t *testing.T) {
	prelude := nodeEq + `
function boardNormId(id){ const s=id==null?'':String(id).trim(); return s==='main'?'':s; }
const boards=[{id:'main',name:'Default'},{id:'seo',name:'SEO'},{id:'aso',name:'ASO'}];
let BOARDS={items:boards,loaded:true};
let ME=null;
const el=id=>({id, value:'', checked:false, disabled:false, hidden:false, dataset:{}});
const ctl={};
const q=(sel)=>ctl[sel.slice(1)]||null;
function tzInputValidate(){ return ''; }
function userRoleField(){ return {}; }
function tr(k){ return k; }
`
	runSPANode(t, prelude, []string{"userBoardAccess", "readUserForm", "syncUserBoards", "boardsNone"}, `
eq(userBoardAccess({type:'USER'}, boards), {all:false, main:true, member:[]}, 'a record without the flag keeps main');
eq(userBoardAccess({type:'USER', board_ids:['aso','main','seo','gone']}, boards), {all:false, main:true, member:['SEO','ASO']}, 'membership names follow the section order, main and deleted ids are skipped');
eq(userBoardAccess({type:'USER', no_main_board:true, board_ids:['seo']}, boards).main, false, 'a cleared mark');
eq(userBoardAccess({type:'ADMIN', no_main_board:true}, boards).all, true, 'an administrator sees every section');
eq(userBoardAccess({type:'USER', all_boards:true}, boards).all, true, 'an all-sections account sees every section');

Object.assign(ctl, {'u-login':el(), 'u-type':el(), 'u-on':el(), 'u-lang':el(), 'u-tz':null, 'u-role':null, 'u-main':el(),
  'u-boards-all':el(), 'u-boards-member':el(), 'u-boards-empty':el()});
ctl['u-login'].value='bob'; ctl['u-type'].value='USER';
ctl['u-main'].checked=false;
eq(readUserForm({}, false, '').no_main_board, true, 'a USER with the mark cleared sends no_main_board');
ctl['u-main'].checked=true;
eq(readUserForm({}, false, '').no_main_board, false, 'a ticked mark keeps main');
ctl['u-type'].value='ADMIN'; ctl['u-main'].checked=false;
eq(readUserForm({}, false, '').no_main_board, false, 'an administrator never sends a cleared mark');

ctl['u-type'].value='USER';
Object.assign(ctl['u-main'], {checked:false, disabled:false, dataset:{main:'0', member:'0'}});
syncUserBoards({});
eq([ctl['u-boards-empty'].hidden, ctl['u-boards-all'].hidden], [false, true], 'no main and no membership warns');
ctl['u-type'].value='ADMIN';
syncUserBoards({});
eq([ctl['u-main'].checked, ctl['u-main'].disabled, ctl['u-boards-all'].hidden, ctl['u-boards-empty'].hidden], [true, true, false, true], 'an administrator gets a locked mark and the all-sections note');
ctl['u-type'].value='USER';
syncUserBoards({});
eq([ctl['u-main'].checked, ctl['u-main'].disabled], [false, false], 'back to USER restores the stored mark');
ctl['u-main'].dataset.member='1';
syncUserBoards({});
eq(ctl['u-boards-empty'].hidden, true, 'membership in another section clears the warning');

ME={uid:'u1', type:'USER'};
BOARDS={items:[], loaded:true};
eq(boardsNone(), true, 'a user with no section gets the notice');
BOARDS={items:[], loaded:true, failed:true};
eq(boardsNone(), false, 'a failed read is not a missing section');
BOARDS={items:boards, loaded:true};
eq(boardsNone(), false, 'a user with sections works as usual');
ME={uid:'a1', type:'ADMIN'}; BOARDS={items:[], loaded:true};
eq(boardsNone(), false, 'an administrator never gets the notice');
if (failed) process.exit(1);
`)
}
