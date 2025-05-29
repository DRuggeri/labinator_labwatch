async function backgroundSend(target) {
    return await fetch(target)
        .then(response => {
            if (!response.ok) {
                return false
            }
            return true
        })
        .catch(error => {
            console.log(error)
            return false
        }
    )
}

function gotoSlide(target) {
    const slide = Reveal.getSlides().find( ( slide ) => {
        return slide.id == target
    });
    if (slide ) {
        i = Reveal.getIndices( slide )
        Reveal.slide(i.h, i.v, i.f)
    } else {
        console.log(`could not find target slide`)
    }
}

async function navigateBottomWindow(url) {
    const target = encodeURIComponent(url)
    await backgroundSend(`http://boss.local:8080/navigate?url=${target}`)
}