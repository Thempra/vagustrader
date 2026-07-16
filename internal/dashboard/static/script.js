(function(){
  function initResize(tableId){
    var table=document.getElementById(tableId);if(!table)return;
    var cols=table.querySelectorAll('colgroup col');
    var saved=localStorage.getItem('colw_'+tableId);
    if(saved){try{var w=JSON.parse(saved);w.forEach(function(v,i){if(cols[i])cols[i].style.width=v+'px';});}catch(e){}}
    var handles=table.querySelectorAll('.resize-handle');
    var dragCol=null,startX=0,startW=0;
    handles.forEach(function(h,i){
      h.addEventListener('mousedown',function(e){
        e.preventDefault();dragCol=i;startX=e.clientX;
        startW=cols[i]?(cols[i].offsetWidth||80):80;
        document.addEventListener('mousemove',onMove);
        document.addEventListener('mouseup',onUp);
      });
    });
    function onMove(e){
      if(dragCol===null)return;
      var nw=Math.max(30,startW+(e.clientX-startX));
      if(cols[dragCol])cols[dragCol].style.width=nw+'px';
    }
    function onUp(){
      if(dragCol!==null){
        var w=[];cols.forEach(function(c){w.push(c.offsetWidth||0);});
        localStorage.setItem('colw_'+tableId,JSON.stringify(w));
      }
      dragCol=null;document.removeEventListener('mousemove',onMove);document.removeEventListener('mouseup',onUp);
    }
  }
  initResize('tbl-pos');initResize('tbl-bal');
  (function(){
    var vd=document.getElementById('vdivider');if(!vd)return;
    var left=vd.previousElementSibling,right=vd.nextElementSibling;
    var main=vd.parentElement,saved=localStorage.getItem('panelSplit');
    if(saved){left.style.flex='0 0 '+saved+'%';}
    var dragging=false,startX=0,startW=0;
    vd.addEventListener('mousedown',function(e){
      e.preventDefault();dragging=true;startX=e.clientX;
      startW=left.getBoundingClientRect().width;
      document.addEventListener('mousemove',onM);document.addEventListener('mouseup',onU);
    });
    function onM(e){
      if(!dragging)return;
      var mw=main.getBoundingClientRect().width;
      var nw=Math.min(85,Math.max(15,(startW+(e.clientX-startX))/mw*100));
      left.style.flex='0 0 '+nw+'%';
    }
    function onU(){
      if(dragging){
        var mw=main.getBoundingClientRect().width;
        var lw=left.getBoundingClientRect().width;
        localStorage.setItem('panelSplit',(lw/mw*100).toFixed(2));
      }
      dragging=false;document.removeEventListener('mousemove',onM);document.removeEventListener('mouseup',onU);
    }
  })();
})();
